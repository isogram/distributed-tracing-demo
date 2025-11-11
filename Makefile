.PHONY: help build up down logs test clean status

# Default target
help: ## Show this help message
	@echo "Distributed Tracing Demo Commands"
	@echo "================================="
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build all services
	docker-compose build

up: ## Start all services
	docker-compose up -d
	@echo "🚀 Services started! Waiting for readiness..."
	@sleep 30
	@echo "✅ Services should be ready now"
	@echo "🌐 Gateway: http://localhost:8080"
	@echo "📊 Grafana: http://localhost:3000 (admin/admin)"

down: ## Stop all services
	docker-compose down

logs: ## Show logs from all services
	docker-compose logs -f

test: ## Run demo test script
	./test-demo.sh

clean: ## Remove all containers, volumes, and images
	docker-compose down -v --rmi all
	docker system prune -f

status: ## Check status of all services
	docker-compose ps

restart: down up ## Restart all services

# Individual service logs
logs-nginx: ## Show nginx logs
	docker-compose logs -f nginx

logs-service-a: ## Show service-a logs
	docker-compose logs -f service-a

logs-service-b: ## Show service-b logs
	docker-compose logs -f service-b

logs-service-c: ## Show service-c logs
	docker-compose logs -f service-c

logs-grafana: ## Show grafana logs
	docker-compose logs -f grafana

logs-tempo: ## Show tempo logs
	docker-compose logs -f tempo

logs-loki: ## Show loki logs
	docker-compose logs -f loki

# Development targets
dev: ## Start services for development (with logs)
	docker-compose up --build

quick-test: ## Quick test of main endpoints
	@echo "Testing main endpoints..."
	@curl -s http://localhost:8080/health | jq .
	@curl -s http://localhost:8080/api/demo/parallel | jq .

test-failures: ## Test failure scenarios
	@echo "Testing failure scenarios..."
	@echo "1. Timeout Failure:"
	@curl -s http://localhost:8080/api/demo/failure/timeout | jq .message
	@echo "2. Partial Failure:"
	@curl -s http://localhost:8080/api/demo/failure/partial | jq .message
	@echo "3. Chain Failure:"
	@curl -s http://localhost:8080/api/demo/failure/chain | jq .message

test-errors: ## Test individual service errors
	@echo "Testing individual service errors..."
	@echo "Service B Database Error:"
	@curl -s http://localhost:8080/service-b/api/db-error | jq .error
	@echo "Service C Auth Error:"
	@curl -s http://localhost:8080/service-c/api/auth-error | jq .error

# Monitoring targets
monitor: ## Open Grafana dashboard
	@echo "Opening Grafana dashboard..."
	@which open >/dev/null && open http://localhost:3000 || echo "Open http://localhost:3000 in your browser"

trace-search: ## Show example trace search commands
	@echo "Trace Search Examples:"
	@echo "====================="
	@echo "In Grafana Explore (Tempo datasource):"
	@echo '  {service.name="service-a"}'
	@echo '  {service.name="service-b"}'
	@echo '  {service.name="service-c"}'
	@echo ""
	@echo "In Grafana Explore (Loki datasource):"
	@echo '  {service="service-a"} |= "trace-"'
	@echo '  {service=~"service-.*"} |= "ERROR"'