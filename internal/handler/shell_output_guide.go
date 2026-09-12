package handler

import (
	"bytes"
	"time"
)

// outputGuidePatterns map raw container-output substrings to a friendly,
// ANSI-colored explanation appended right after them (タスク指示書「危険
// コマンドの親切なUI警告およびバックエンドエラー注釈(ガイド)機能」§2
// タスク2)。These are advisory only: they never change whether a command
// succeeds or fails - that's still decided entirely by
// seccomp/CapDrop/cgroups/ディスククォータ, exactly as before this feature
// existed. A guide is just appended to the stream right after the output
// that triggered it; the original bytes are always forwarded first,
// unmodified.
type outputGuidePattern struct {
	name    string // cooldown key / logging
	needle  []byte
	message []byte
}

var outputGuidePatterns = []outputGuidePattern{
	{
		name:    "seccomp_denied",
		needle:  []byte("Operation not permitted"),
		message: []byte("\r\n\x1b[33m💡 [ガイド]: システム保護(seccomp/CapDrop)により、特権コマンドの実行がブロックされました。\x1b[0m\r\n"),
	},
	{
		name:    "disk_quota",
		needle:  []byte("No space left on device"),
		message: []byte("\r\n\x1b[33m💡 [ガイド]: 割り当てられたディスク容量(クォータ制限)の上限に達しました。不要なファイルを削除してください。\x1b[0m\r\n"),
	},
	{
		name:    "oom_killed",
		needle:  []byte("Killed"),
		message: []byte("\r\n\x1b[33m💡 [ガイド]: メモリ使用量が制限(512MB)を超過したため、プロセスが停止しました。\x1b[0m\r\n"),
	},
	{
		name:    "oom_out_of_memory",
		needle:  []byte("Out of memory"),
		message: []byte("\r\n\x1b[33m💡 [ガイド]: メモリ使用量が制限(512MB)を超過したため、プロセスが停止しました。\x1b[0m\r\n"),
	},
}

// maxGuideNeedleLen is the longest pattern above, used to size the
// boundary-carry window in outputGuideScanner.
var maxGuideNeedleLen = func() int {
	max := 0
	for _, p := range outputGuidePatterns {
		if len(p.needle) > max {
			max = len(p.needle)
		}
	}
	return max
}()

// guideCooldown rate-limits repeat guides for the same pattern within one
// session (e.g. a runaway process printing "Killed" many times shouldn't
// flood the terminal with the same tip over and over).
const guideCooldown = 3 * time.Second

// outputGuideScanner watches the raw bytes already forwarded to the browser
// for the substrings in outputGuidePatterns and reports which guide
// messages to send as immediate follow-up frames. It never delays or
// modifies the original output - the caller always forwards the chunk
// first, unbuffered (see relayShell), so interactive echo latency is
// unaffected; this only decides what (if anything) to append right after.
//
// tail keeps the last (maxGuideNeedleLen-1) bytes across calls so a pattern
// split across two Read()s isn't missed. Only the small
// tail+chunk-prefix "boundary" window is re-scanned for cross-boundary
// matches, so a match already found via the previous chunk's own full scan
// is never counted twice.
type outputGuideScanner struct {
	tail        []byte
	lastEmitted map[string]time.Time
}

func newOutputGuideScanner() *outputGuideScanner {
	return &outputGuideScanner{lastEmitted: make(map[string]time.Time)}
}

// Scan inspects chunk (bytes just forwarded to the client) and returns the
// guide messages, cooldown-filtered and deduped within this call, to send
// as follow-up frames.
func (s *outputGuideScanner) Scan(chunk []byte) [][]byte {
	var guides [][]byte
	reported := make(map[string]bool) // 同一チャンク内で同じパターンが複数回出ても1回にまとめる

	report := func(p outputGuidePattern) {
		if reported[p.name] {
			return
		}
		if last, ok := s.lastEmitted[p.name]; ok && time.Since(last) < guideCooldown {
			return
		}
		reported[p.name] = true
		s.lastEmitted[p.name] = time.Now()
		guides = append(guides, p.message)
	}

	for _, p := range outputGuidePatterns {
		if bytes.Contains(chunk, p.needle) {
			report(p)
			continue
		}
		if len(s.tail) == 0 {
			continue
		}
		// chunk単独には無いが、前回チャンク末尾(tail)から今回チャンク先頭へ
		// 分割されて出現した可能性を、境界だけの小さな窓で確認する。
		prefixLen := len(p.needle) - 1
		if prefixLen > len(chunk) {
			prefixLen = len(chunk)
		}
		boundary := append(append([]byte{}, s.tail...), chunk[:prefixLen]...)
		if idx := bytes.Index(boundary, p.needle); idx != -1 && idx+len(p.needle) > len(s.tail) {
			report(p)
		}
	}

	tailLen := maxGuideNeedleLen - 1
	combined := append(append([]byte{}, s.tail...), chunk...)
	if len(combined) > tailLen {
		combined = combined[len(combined)-tailLen:]
	}
	s.tail = combined

	return guides
}
