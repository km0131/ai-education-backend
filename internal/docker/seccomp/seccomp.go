// Package seccomp provides the seccomp profile applied to student sandbox
// containers (NextPlan.md フェーズ2: 「seccompプロファイルで危険なシステムコール
// (ptrace、mount等)をブロック」).
//
// The profile in sandbox-deny.json is a denylist (defaultAction
// SCMP_ACT_ALLOW, with an explicit ERRNO list for syscalls that let a
// process escape/attack the host: ptrace-based process inspection, mount/
// namespace manipulation, kernel module loading, clock changes, etc). A
// denylist is used instead of an allowlist so that ordinary sandbox tooling
// (apt/pip/npm/go install, per NextPlan.md フェーズ1's package-manager
// check) keeps working without needing to hand-maintain a full syscall
// allowlist - the blocked set mirrors the syscalls Docker's own bundled
// default seccomp profile already denies, which is why it's already known
// not to break those package managers.
//
// This sits alongside --cap-drop=ALL and the gVisor(runsc) runtime as one
// more independent layer of the 多層防御 (defense-in-depth) design from
// NextPlan.md §3.5: even if a capability were ever accidentally restored,
// these syscalls would still be rejected at the seccomp filter.
package seccomp

import _ "embed"

//go:embed sandbox-deny.json
var profileJSON string

// SecurityOpt returns the HostConfig.SecurityOpt value that applies the
// sandbox seccomp profile to a container, in the same "seccomp=<json>" form
// the Docker CLI's --security-opt flag accepts.
func SecurityOpt() []string {
	return []string{"seccomp=" + profileJSON}
}
