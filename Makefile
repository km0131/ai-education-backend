# salon-app/Makefile

.PHONY: gen-api

gen-api:
	@echo "--- Generating Swagger ---"
	# --dir ./ を追加して backend フォルダ全体をスキャン対象にする
	cd backend && CGO_ENABLED=0 swag init -g cmd/main.go --dir ./ --output docs

	@echo "--- Converting Swagger 2.0 to OpenAPI 3.0 ---"
	cd backend && npx swagger2openapi docs/swagger.yaml -o docs/openapi.yaml --yaml

	@echo "--- Generating TypeScript types from OpenAPI 3.0 ---"
	mkdir -p frontend/src/api
	cd frontend && npx openapi-typescript ../backend/docs/openapi.yaml -o src/api/api.d.ts