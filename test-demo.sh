#!/bin/bash

# Distributed Tracing Demo Test Script
echo "🧪 Distributed Tracing Demo Test Script"
echo "======================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Base URL
BASE_URL="http://localhost:8080"

# Function to make request and show trace ID
test_endpoint() {
    local endpoint=$1
    local description=$2
    
    echo -e "\n${YELLOW}Testing: $description${NC}"
    echo "Endpoint: $endpoint"
    
    response=$(curl -s $endpoint)
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}✅ Success${NC}"
        trace_id=$(echo $response | grep -o '"trace_id":"[^"]*"' | cut -d'"' -f4)
        if [ ! -z "$trace_id" ]; then
            echo "🔍 Trace ID: $trace_id"
        fi
        echo "📄 Response: $(echo $response | jq -r '.message // .error // "No message field"' 2>/dev/null || echo "JSON parse error")"
    else
        echo -e "${RED}❌ Failed${NC}"
    fi
}

# Function to check service health
check_health() {
    local service=$1
    local url=$2
    
    echo -n "Checking $service... "
    if curl -s $url > /dev/null; then
        echo -e "${GREEN}✅${NC}"
        return 0
    else
        echo -e "${RED}❌${NC}"
        return 1
    fi
}

echo -e "\n🏥 Health Checks"
echo "=================="

# Check if services are running
all_healthy=true

check_health "Nginx Gateway" "$BASE_URL/health" || all_healthy=false
check_health "Service A" "$BASE_URL/service-a/health" || all_healthy=false  
check_health "Service B" "$BASE_URL/service-b/health" || all_healthy=false
check_health "Service C" "$BASE_URL/service-c/health" || all_healthy=false
check_health "Grafana" "http://localhost:3000/api/health" || all_healthy=false

if [ "$all_healthy" = false ]; then
    echo -e "\n${RED}⚠️  Some services are not healthy. Please check docker-compose logs.${NC}"
    echo "Run: docker-compose logs [service-name]"
    exit 1
fi

echo -e "\n🚀 Running Demo Scenarios"
echo "=========================="

# Test scenarios
test_endpoint "$BASE_URL/api/demo/parallel" "Parallel Calls (A → B & C simultaneously)"
test_endpoint "$BASE_URL/api/demo/sequential" "Sequential Calls (A → B → C sequentially)"  

echo -e "\n🔄 Testing Circular Call Scenarios"
echo "==================================="
test_endpoint "$BASE_URL/api/demo/circular" "Circular Call (C → A)"
test_endpoint "$BASE_URL/api/demo/circular-b-to-a" "Circular Call (B → A)"
test_endpoint "$BASE_URL/api/demo/circular-b-to-c" "Circular Call (B → C)"

echo -e "\n🔍 Testing Individual Services"
echo "==============================="

test_endpoint "$BASE_URL/service-a/api/process" "Service A Direct"
test_endpoint "$BASE_URL/service-b/api/process" "Service B Direct"
test_endpoint "$BASE_URL/service-c/api/process" "Service C Direct"

echo -e "\n📊 Testing Data Endpoints"
echo "=========================="

test_endpoint "$BASE_URL/service-b/api/data" "Service B Data"
test_endpoint "$BASE_URL/service-c/api/data" "Service C Data"

echo -e "\n🔥 Testing Failure Scenarios"
echo "============================="

test_endpoint "$BASE_URL/api/demo/failure/timeout" "Timeout Failure Scenario"
test_endpoint "$BASE_URL/api/demo/failure/partial" "Partial Failure Scenario"
test_endpoint "$BASE_URL/api/demo/failure/cascade" "Cascade Failure Scenario"
test_endpoint "$BASE_URL/api/demo/failure/chain" "Chain Failure Scenario"

echo -e "\n💥 Testing Individual Service Errors"
echo "======================================"

test_endpoint "$BASE_URL/service-b/api/error" "Service B Random Error"
test_endpoint "$BASE_URL/service-b/api/db-error" "Service B Database Error"
test_endpoint "$BASE_URL/service-b/api/memory-error" "Service B Memory Error"
test_endpoint "$BASE_URL/service-c/api/error" "Service C Random Error"
test_endpoint "$BASE_URL/service-c/api/auth-error" "Service C Auth Error"
test_endpoint "$BASE_URL/service-c/api/rate-limit-error" "Service C Rate Limit Error"
test_endpoint "$BASE_URL/service-c/api/dependency-error" "Service C Dependency Error"

echo "✅ Test Summary"
echo "================"
echo "🌐 Gateway: $BASE_URL"
echo "📊 Grafana v12.2.0: http://localhost:3000 (admin/admin)"
echo "🔍 Tempo v2.9.0: http://localhost:3200"
echo "📝 Loki v3.5.8: http://localhost:3100"

echo -e "\n📋 Next Steps:"
echo "1. Open Grafana at http://localhost:3000"
echo "2. Navigate to Dashboards > Distributed Tracing Demo"
echo "3. Use trace IDs from above responses to search traces"
echo "4. Explore logs in Grafana using Loki datasource"
echo "5. Check traceparent headers in circular calls:"
echo "   - C → A: /api/demo/circular"
echo "   - B → A: /api/demo/circular-b-to-a"
echo "   - B → C: /api/demo/circular-b-to-c"

echo -e "\n🎉 Demo test completed!"