package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type Response struct {
	Service   string      `json:"service"`
	Message   string      `json:"message"`
	TraceID   string      `json:"trace_id"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data,omitempty"`
}

type ServiceResponse struct {
	Service   string    `json:"service"`
	Message   string    `json:"message"`
	TraceID   string    `json:"trace_id"`
	Timestamp time.Time `json:"timestamp"`
}

var (
	serviceBURL = getEnv("SERVICE_B_URL", "http://service-b:3000")
	serviceCURL = getEnv("SERVICE_C_URL", "http://service-c:5000")
	tracer      = otel.Tracer("service-a")
	httpClient  = &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   10 * time.Second,
	}
)

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func initTracer() func() {
	ctx := context.Background()

	// Create OTLP trace exporter
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317")),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		log.Fatalf("Failed to create trace exporter: %v", err)
	}

	// Create resource
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String("service-a"),
			semconv.ServiceVersionKey.String("1.0.0"),
		),
	)
	if err != nil {
		log.Fatalf("Failed to create resource: %v", err)
	}

	// Create trace provider
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return func() {
		if err := tp.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
	}
}

func tracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log incoming headers for debugging
		log.Printf("Incoming headers: traceparent=%s, x-trace-id=%s",
			r.Header.Get("traceparent"), r.Header.Get("X-Trace-ID"))

		// Extract OpenTelemetry context from incoming headers if present
		// This ensures we continue an existing distributed trace
		parentCtx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Check if we have a valid parent trace context
		parentSpan := oteltrace.SpanFromContext(parentCtx)
		if parentSpan.SpanContext().IsValid() {
			log.Printf("Extracted parent OpenTelemetry context from headers")
			r = r.WithContext(parentCtx)
		} else {
			log.Printf("No valid parent context found, using current context")
		}

		// Extract or generate trace ID
		traceID := r.Header.Get("X-Trace-ID")
		if traceID == "" {
			traceID = generateTraceID()
			log.Printf("Generated new trace ID: %s", traceID)
		}

		// Add trace ID to context and response
		ctx := context.WithValue(r.Context(), "trace_id", traceID)
		w.Header().Set("X-Trace-ID", traceID)

		// Add trace ID to current span (this should now be properly connected to parent)
		if span := oteltrace.SpanFromContext(ctx); span.IsRecording() {
			span.SetAttributes(attribute.String("trace.id", traceID))
			span.SetAttributes(attribute.String("http.method", r.Method))
			span.SetAttributes(attribute.String("http.url", r.URL.String()))
		}

		log.Printf("[%s] %s %s - Processing request", traceID, r.Method, r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func generateTraceID() string {
	return fmt.Sprintf("trace-%d-%s", time.Now().UnixNano(), uuid.New().String()[:8])
}

func makeRequest(ctx context.Context, method, url string, traceID string) (*ServiceResponse, error) {
	span := oteltrace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("http.method", method),
		attribute.String("http.url", url),
		attribute.String("trace.id", traceID),
	)

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Propagate trace ID
	req.Header.Set("X-Trace-ID", traceID)
	req.Header.Set("Content-Type", "application/json")

	// Propagate OpenTelemetry span context
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	log.Printf("[%s] Making %s request to %s", traceID, method, url)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var serviceResp ServiceResponse
	if err := json.Unmarshal(body, &serviceResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	log.Printf("[%s] Received response from %s: %s", traceID, serviceResp.Service, serviceResp.Message)
	return &serviceResp, nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	_, span := tracer.Start(r.Context(), "health_check")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))

	response := Response{
		Service:   "service-a",
		Message:   "Service A is healthy",
		TraceID:   traceID,
		Timestamp: time.Now(),
	}

	log.Printf("[%s] Health check completed", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func parallelHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	ctx, span := tracer.Start(r.Context(), "parallel_calls")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))
	log.Printf("[%s] Starting parallel calls to Service B and C", traceID)

	var wg sync.WaitGroup
	var respB, respC *ServiceResponse
	var errB, errC error

	// Call Service B
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, spanB := tracer.Start(ctx, "call_service_b")
		defer spanB.End()
		respB, errB = makeRequest(ctx, "GET", serviceBURL+"/api/process", traceID)
	}()

	// Call Service C
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, spanC := tracer.Start(ctx, "call_service_c")
		defer spanC.End()
		respC, errC = makeRequest(ctx, "GET", serviceCURL+"/api/process", traceID)
	}()

	wg.Wait()

	responses := map[string]interface{}{}
	if errB == nil {
		responses["service_b"] = respB
	} else {
		responses["service_b_error"] = errB.Error()
		log.Printf("[%s] Error calling Service B: %v", traceID, errB)
	}

	if errC == nil {
		responses["service_c"] = respC
	} else {
		responses["service_c_error"] = errC.Error()
		log.Printf("[%s] Error calling Service C: %v", traceID, errC)
	}

	response := Response{
		Service:   "service-a",
		Message:   "Parallel calls completed",
		TraceID:   traceID,
		Timestamp: time.Now(),
		Data:      responses,
	}

	log.Printf("[%s] Parallel calls completed successfully", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func sequentialHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	ctx, span := tracer.Start(r.Context(), "sequential_calls")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))
	log.Printf("[%s] Starting sequential calls to Service B and C", traceID)

	// Call Service B first
	ctx, spanB := tracer.Start(ctx, "call_service_b")
	respB, errB := makeRequest(ctx, "GET", serviceBURL+"/api/process", traceID)
	spanB.End()

	if errB != nil {
		log.Printf("[%s] Error calling Service B: %v", traceID, errB)
		http.Error(w, fmt.Sprintf("Failed to call Service B: %v", errB), http.StatusInternalServerError)
		return
	}

	// Then call Service C
	ctx, spanC := tracer.Start(ctx, "call_service_c")
	respC, errC := makeRequest(ctx, "GET", serviceCURL+"/api/process", traceID)
	spanC.End()

	if errC != nil {
		log.Printf("[%s] Error calling Service C: %v", traceID, errC)
		http.Error(w, fmt.Sprintf("Failed to call Service C: %v", errC), http.StatusInternalServerError)
		return
	}

	response := Response{
		Service:   "service-a",
		Message:   "Sequential calls completed",
		TraceID:   traceID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"service_b": respB,
			"service_c": respC,
		},
	}

	log.Printf("[%s] Sequential calls completed successfully", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func processHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	_, span := tracer.Start(r.Context(), "process_request")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))

	// Simulate some processing
	time.Sleep(100 * time.Millisecond)

	response := Response{
		Service:   "service-a",
		Message:   "Request processed by Service A",
		TraceID:   traceID,
		Timestamp: time.Now(),
	}

	log.Printf("[%s] Processed request in Service A", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func timeoutFailureHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	ctx, span := tracer.Start(r.Context(), "timeout_failure_scenario")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))
	log.Printf("[%s] Starting timeout failure scenario", traceID)

	var wg sync.WaitGroup
	var respB, respC *ServiceResponse
	var errB, errC error

	// Call Service B normally
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, spanB := tracer.Start(ctx, "call_service_b_normal")
		defer spanB.End()
		respB, errB = makeRequest(ctx, "GET", serviceBURL+"/api/process", traceID)
	}()

	// Call Service C with error endpoint to simulate failure
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, spanC := tracer.Start(ctx, "call_service_c_error")
		defer spanC.End()
		respC, errC = makeRequest(ctx, "GET", serviceCURL+"/api/timeout", traceID)
	}()

	wg.Wait()

	responses := map[string]interface{}{}
	if errB == nil {
		responses["service_b"] = respB
		log.Printf("[%s] Service B call succeeded", traceID)
	} else {
		responses["service_b_error"] = errB.Error()
		log.Printf("[%s] Service B call failed: %v", traceID, errB)
		span.RecordError(errB)
	}

	if errC == nil {
		responses["service_c"] = respC
		log.Printf("[%s] Service C call succeeded", traceID)
	} else {
		responses["service_c_error"] = errC.Error()
		log.Printf("[%s] Service C call failed: %v", traceID, errC)
		span.RecordError(errC)
	}

	response := Response{
		Service:   "service-a",
		Message:   "Timeout failure scenario completed",
		TraceID:   traceID,
		Timestamp: time.Now(),
		Data:      responses,
	}

	log.Printf("[%s] Timeout failure scenario completed", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func partialFailureHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	ctx, span := tracer.Start(r.Context(), "partial_failure_scenario")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))
	log.Printf("[%s] Starting partial failure scenario", traceID)

	// First call Service B (should succeed)
	ctx, spanB := tracer.Start(ctx, "call_service_b_success")
	respB, errB := makeRequest(ctx, "GET", serviceBURL+"/api/process", traceID)
	spanB.End()

	if errB != nil {
		log.Printf("[%s] Unexpected error in Service B: %v", traceID, errB)
		http.Error(w, fmt.Sprintf("Unexpected failure in Service B: %v", errB), http.StatusInternalServerError)
		return
	}

	// Then call Service C with error endpoint (should fail)
	ctx, spanC := tracer.Start(ctx, "call_service_c_intentional_error")
	_, errC := makeRequest(ctx, "GET", serviceCURL+"/api/error", traceID)
	spanC.End()

	var serviceCError string
	if errC != nil {
		serviceCError = errC.Error()
	} else {
		serviceCError = "No error occurred (unexpected)"
	}

	response := Response{
		Service:   "service-a",
		Message:   "Partial failure scenario - Service B succeeded, Service C failed",
		TraceID:   traceID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"service_b_success": respB,
			"service_c_error":   serviceCError,
			"scenario":          "partial_failure",
		},
	}

	if errC != nil {
		span.RecordError(errC)
		log.Printf("[%s] Expected error in Service C: %v", traceID, errC)
	}

	log.Printf("[%s] Partial failure scenario completed successfully", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func cascadeFailureHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	ctx, span := tracer.Start(r.Context(), "cascade_failure_scenario")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))
	log.Printf("[%s] Starting cascade failure scenario", traceID)

	// Call Service B first
	ctx, spanB := tracer.Start(ctx, "call_service_b_before_cascade")
	respB, errB := makeRequest(ctx, "GET", serviceBURL+"/api/process", traceID)
	spanB.End()

	if errB != nil {
		log.Printf("[%s] Service B failed, cascading failure: %v", traceID, errB)
		span.RecordError(errB)
		http.Error(w, fmt.Sprintf("Cascade failure started at Service B: %v", errB), http.StatusInternalServerError)
		return
	}

	// Now call Service C which will call Service A (circular), but we'll make it fail
	ctx, spanC := tracer.Start(ctx, "call_service_c_cascade")
	respC, errC := makeRequest(ctx, "GET", serviceCURL+"/api/call-service-a-error", traceID)
	spanC.End()

	if errC != nil {
		log.Printf("[%s] Cascade failure propagated through Service C: %v", traceID, errC)
		span.RecordError(errC)
		http.Error(w, fmt.Sprintf("Cascade failure propagated: %v", errC), http.StatusInternalServerError)
		return
	}

	response := Response{
		Service:   "service-a",
		Message:   "Cascade failure scenario completed",
		TraceID:   traceID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"service_b": respB,
			"service_c": respC,
		},
	}

	log.Printf("[%s] Cascade failure scenario completed unexpectedly successfully", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func chainFailureHandler(w http.ResponseWriter, r *http.Request) {
	traceID := r.Context().Value("trace_id").(string)
	ctx, span := tracer.Start(r.Context(), "chain_failure_scenario")
	defer span.End()

	span.SetAttributes(attribute.String("trace.id", traceID))
	log.Printf("[%s] Starting chain failure scenario", traceID)

	// Sequential calls with failures at different points

	// Step 1: Call Service B (should succeed)
	ctx, spanB1 := tracer.Start(ctx, "chain_step_1_service_b")
	respB1, errB1 := makeRequest(ctx, "GET", serviceBURL+"/api/process", traceID)
	spanB1.End()

	if errB1 != nil {
		log.Printf("[%s] Chain failure at step 1 (Service B): %v", traceID, errB1)
		span.RecordError(errB1)
		http.Error(w, fmt.Sprintf("Chain broken at step 1: %v", errB1), http.StatusInternalServerError)
		return
	}

	// Step 2: Call Service C (should succeed)
	ctx, spanC1 := tracer.Start(ctx, "chain_step_2_service_c")
	respC1, errC1 := makeRequest(ctx, "GET", serviceCURL+"/api/process", traceID)
	spanC1.End()

	if errC1 != nil {
		log.Printf("[%s] Chain failure at step 2 (Service C): %v", traceID, errC1)
		span.RecordError(errC1)
		http.Error(w, fmt.Sprintf("Chain broken at step 2: %v", errC1), http.StatusInternalServerError)
		return
	}

	// Step 3: Call Service B with error (should fail)
	ctx, spanB2 := tracer.Start(ctx, "chain_step_3_service_b_error")
	respB2, errB2 := makeRequest(ctx, "GET", serviceBURL+"/api/error", traceID)
	spanB2.End()

	// This step is expected to fail
	if errB2 != nil {
		log.Printf("[%s] Expected chain failure at step 3 (Service B error): %v", traceID, errB2)
		span.RecordError(errB2)

		response := Response{
			Service:   "service-a",
			Message:   "Chain failure scenario - failed at step 3 as expected",
			TraceID:   traceID,
			Timestamp: time.Now(),
			Data: map[string]interface{}{
				"step_1_success": respB1,
				"step_2_success": respC1,
				"step_3_failure": errB2.Error(),
				"scenario":       "chain_failure_at_step_3",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPartialContent) // 206 to indicate partial success
		json.NewEncoder(w).Encode(response)
		return
	}

	// If we get here, something unexpected happened
	response := Response{
		Service:   "service-a",
		Message:   "Chain failure scenario completed unexpectedly without failure",
		TraceID:   traceID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"step_1": respB1,
			"step_2": respC1,
			"step_3": respB2,
		},
	}

	log.Printf("[%s] Chain failure scenario completed without expected failure", traceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func main() {
	// Initialize tracing
	shutdown := initTracer()
	defer shutdown()

	// Create router
	r := mux.NewRouter()

	// Add OpenTelemetry middleware
	r.Use(otelmux.Middleware("service-a"))
	r.Use(tracingMiddleware)

	// Routes
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/api/parallel", parallelHandler).Methods("GET")
	r.HandleFunc("/api/sequential", sequentialHandler).Methods("GET")
	r.HandleFunc("/api/process", processHandler).Methods("GET")

	// Failure scenario routes
	r.HandleFunc("/api/failure/timeout", timeoutFailureHandler).Methods("GET")
	r.HandleFunc("/api/failure/partial", partialFailureHandler).Methods("GET")
	r.HandleFunc("/api/failure/cascade", cascadeFailureHandler).Methods("GET")
	r.HandleFunc("/api/failure/chain", chainFailureHandler).Methods("GET")

	log.Println("Service A starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
