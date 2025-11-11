import os
import json
import time
import uuid
import logging
import requests
from datetime import datetime
from typing import Optional, Dict, Any
from flask import Flask, request, jsonify

# OpenTelemetry imports
from opentelemetry import trace, baggage
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.semconv.resource import ResourceAttributes
from opentelemetry.instrumentation.flask import FlaskInstrumentor
from opentelemetry.instrumentation.requests import RequestsInstrumentor
from opentelemetry.propagate import inject, extract, set_global_textmap
from opentelemetry import context
from opentelemetry.propagators.composite import CompositePropagator  
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator
from opentelemetry.baggage.propagation import W3CBaggagePropagator
from typing import Optional

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(message)s'
)
logger = logging.getLogger(__name__)

# Custom propagator to handle duplicate traceparent headers
class CleanTraceContextPropagator(TraceContextTextMapPropagator):
    def extract(self, carrier, context: Optional[context.Context] = None, getter=None):
        """Extract context, cleaning duplicate traceparent headers first"""
        if getter is None:
            getter = self._default_getter
        
        # Clean up carrier if it has duplicate traceparent headers
        cleaned_carrier = {}
        for key, value in carrier.items() if hasattr(carrier, 'items') else [(k, getter.get(carrier, k)[0]) for k in getter.keys(carrier)]:
            if key.lower() == 'traceparent' and isinstance(value, str) and ',' in value:
                # Take the first traceparent value if there are duplicates
                cleaned_carrier[key] = value.split(',')[0].strip()
            else:
                cleaned_carrier[key] = value
        
        return super().extract(cleaned_carrier, context, getter)

class StructuredLogger:
    def __init__(self, service_name: str):
        self.service_name = service_name
    
    def _log(self, level: str, message: str, trace_id: Optional[str] = None, **kwargs):
        log_entry = {
            'timestamp': datetime.utcnow().isoformat() + 'Z',
            'level': level.upper(),
            'message': message,
            'service': self.service_name,
            'trace_id': trace_id,
            **kwargs
        }
        print(json.dumps(log_entry))
    
    def info(self, message: str, trace_id: Optional[str] = None, **kwargs):
        self._log('info', message, trace_id, **kwargs)
    
    def error(self, message: str, trace_id: Optional[str] = None, **kwargs):
        self._log('error', message, trace_id, **kwargs)
    
    def warning(self, message: str, trace_id: Optional[str] = None, **kwargs):
        self._log('warning', message, trace_id, **kwargs)

# Initialize structured logger
structured_logger = StructuredLogger('service-c')

# Initialize OpenTelemetry
def init_tracer():
    # Create resource
    resource = Resource.create({
        ResourceAttributes.SERVICE_NAME: "service-c",
        ResourceAttributes.SERVICE_VERSION: "1.0.0"
    })
    
    # Create tracer provider
    trace.set_tracer_provider(TracerProvider(resource=resource))
    tracer_provider = trace.get_tracer_provider()
    
    # Create OTLP exporter
    otlp_exporter = OTLPSpanExporter(
        endpoint=os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://tempo:4317"),
        insecure=True
    )
    
    # Add span processor
    span_processor = BatchSpanProcessor(otlp_exporter)
    tracer_provider.add_span_processor(span_processor)
    
    # Configure propagators to handle tracing context with duplicate header cleanup
    propagators = [
        CleanTraceContextPropagator(),  # Custom propagator that handles duplicate headers
        W3CBaggagePropagator()
    ]
    set_global_textmap(CompositePropagator(propagators))

init_tracer()

# Get tracer
tracer = trace.get_tracer(__name__)

# Create Flask app
app = Flask(__name__)

# Custom middleware to clean headers before Flask auto-instrumentation processes them
class CleanHeadersMiddleware:
    def __init__(self, app):
        self.app = app
    
    def __call__(self, environ, start_response):
        # Clean up traceparent header before Flask processes it
        if 'HTTP_TRACEPARENT' in environ:
            traceparent = environ['HTTP_TRACEPARENT']
            if ',' in traceparent:
                # Take only the first traceparent value
                cleaned_traceparent = traceparent.split(',')[0].strip()
                environ['HTTP_TRACEPARENT'] = cleaned_traceparent
        
        return self.app(environ, start_response)

# Apply the middleware
app.wsgi_app = CleanHeadersMiddleware(app.wsgi_app)

# Auto-instrument Flask and Requests
FlaskInstrumentor().instrument_app(app)
RequestsInstrumentor().instrument()

# Configuration
SERVICE_A_URL = os.getenv('SERVICE_A_URL', 'http://service-a:8080')

def generate_trace_id() -> str:
    """Generate a fallback trace ID"""
    return f"fallback-{int(time.time() * 1000)}-{str(uuid.uuid4())[:8]}"

def extract_trace_id() -> str:
    """Extract trace ID from request headers or generate fallback"""
    trace_id = request.headers.get('X-Trace-ID') or request.headers.get('X-Amzn-Trace-Id')
    
    if not trace_id:
        trace_id = generate_trace_id()
        structured_logger.warning(
            "Missing trace ID from upstream - generated fallback",
            trace_id=trace_id,
            headers=dict(request.headers),
            path=request.path
        )
    
    return trace_id

def make_request_to_service_a(trace_id: str, endpoint: str = '/api/process') -> Dict[str, Any]:
    """Make a request to Service A with proper trace propagation"""
    url = f"{SERVICE_A_URL}{endpoint}"
    
    with tracer.start_as_current_span("call_service_a") as span:
        span.set_attribute("trace.id", trace_id)
        span.set_attribute("http.method", "GET")
        span.set_attribute("http.url", url)
        
        headers = {
            'X-Trace-ID': trace_id,
            'Content-Type': 'application/json'
        }
        
        # Inject OpenTelemetry context into headers
        inject(headers)
        
        # Debug: Log what headers we're sending
        structured_logger.info(
            f"Outgoing headers to Service A",
            trace_id=trace_id,
            headers=headers
        )
        
        structured_logger.info(
            f"Making request to Service A: {endpoint}",
            trace_id=trace_id,
            url=url
        )
        
        try:
            response = requests.get(url, headers=headers, timeout=10)
            response.raise_for_status()
            
            result = response.json()
            
            structured_logger.info(
                "Received response from Service A",
                trace_id=trace_id,
                status_code=response.status_code,
                response_service=result.get('service', 'unknown')
            )
            
            return result
            
        except requests.exceptions.RequestException as e:
            error_msg = f"Request to Service A failed: {str(e)}"
            structured_logger.error(
                error_msg,
                trace_id=trace_id,
                error_type=type(e).__name__,
                url=url
            )
            span.record_exception(e)
            raise Exception(error_msg)

@app.before_request
def before_request():
    """Extract trace ID and add custom attributes (Flask auto-instrumentation handles context with cleaned headers)"""
    # Debug: Log incoming headers to verify traceparent is received
    headers_dict = dict(request.headers)
    structured_logger.info(
        "Incoming headers",
        headers=headers_dict,
        path=request.path
    )
    
    # Extract our custom trace ID
    trace_id = extract_trace_id()
    request.trace_id = trace_id
    
    # Add custom attributes to the current span (created by Flask auto-instrumentation with cleaned headers)
    span = trace.get_current_span()
    if span.is_recording():
        span.set_attribute("trace.id", trace_id)
        span.set_attribute("http.method", request.method)
        span.set_attribute("http.url", request.url)
        span.set_attribute("http.route", request.endpoint or request.path)
        span.set_attribute("service.name", "service-c")
        
        # Check if we have a valid parent span context
        span_context = span.get_span_context()
        if span_context.trace_id != 0:
            structured_logger.info("Flask auto-instrumentation created span with parent context",
                                 otel_trace_id=format(span_context.trace_id, '032x'))
        else:
            structured_logger.warning("Flask auto-instrumentation created root span (no parent)")
    
    structured_logger.info(
        f"Processing {request.method} {request.path}",
        trace_id=trace_id,
        method=request.method,
        path=request.path,
        user_agent=request.headers.get('User-Agent', 'unknown')
    )

@app.after_request
def after_request(response):
    """Log response with trace information"""
    # Get the custom trace ID
    trace_id = getattr(request, 'trace_id', 'unknown')
    
    # Add response headers
    if hasattr(request, 'trace_id'):
        response.headers['X-Trace-ID'] = request.trace_id
    
    # Add response attributes to current span (managed by Flask auto-instrumentation)
    span = trace.get_current_span()
    if span.is_recording():
        span.set_attribute("http.status_code", response.status_code)
    
    structured_logger.info(
        f"Completed {request.method} {request.path}",
        trace_id=trace_id,
        status_code=response.status_code,
        method=request.method,
        path=request.path
    )
    
    return response

@app.route('/health', methods=['GET'])
def health():
    """Health check endpoint"""
    with tracer.start_as_current_span("health_check") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "health_check")
        
        response = {
            'service': 'service-c',
            'message': 'Service C is healthy',
            'trace_id': request.trace_id,
            'timestamp': datetime.utcnow().isoformat() + 'Z'
        }
        
        structured_logger.info("Health check completed", trace_id=request.trace_id)
        return jsonify(response)

@app.route('/api/process', methods=['GET'])
def process():
    """Process request endpoint"""
    with tracer.start_as_current_span("process_request") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "process_request")
        
        structured_logger.info("Processing request in Service C", trace_id=request.trace_id)
        
        # Simulate processing time
        processing_time = (time.time() % 0.2) + 0.1  # 0.1-0.3 seconds
        time.sleep(processing_time)
        
        response = {
            'service': 'service-c',
            'message': 'Request processed by Service C (Python)',
            'trace_id': request.trace_id,
            'timestamp': datetime.utcnow().isoformat() + 'Z',
            'processing_time_ms': round(processing_time * 1000),
            'data': {
                'processed_records': int(time.time()) % 500 + 1,
                'status': 'success',
                'python_version': '3.9+'
            }
        }
        
        structured_logger.info(
            "Request processed successfully",
            trace_id=request.trace_id,
            processing_time_ms=response['processing_time_ms'],
            processed_records=response['data']['processed_records']
        )
        
        return jsonify(response)

@app.route('/api/call-service-a', methods=['GET'])
def call_service_a():
    """Call Service A endpoint - demonstrates circular calls"""
    with tracer.start_as_current_span("call_service_a_endpoint") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "call_service_a_endpoint")
        
        structured_logger.info(
            "Service C calling Service A",
            trace_id=request.trace_id
        )
        
        try:
            # Call Service A
            service_a_response = make_request_to_service_a(request.trace_id)
            
            response = {
                'service': 'service-c',
                'message': 'Service C successfully called Service A',
                'trace_id': request.trace_id,
                'timestamp': datetime.utcnow().isoformat() + 'Z',
                'service_a_response': service_a_response
            }
            
            structured_logger.info(
                "Successfully called Service A from Service C",
                trace_id=request.trace_id
            )
            
            return jsonify(response)
            
        except Exception as e:
            error_response = {
                'service': 'service-c',
                'error': 'Failed to call Service A',
                'message': str(e),
                'trace_id': request.trace_id,
                'timestamp': datetime.utcnow().isoformat() + 'Z'
            }
            
            structured_logger.error(
                "Failed to call Service A",
                trace_id=request.trace_id,
                error=str(e)
            )
            
            span.record_exception(e)
            return jsonify(error_response), 500

@app.route('/api/data', methods=['GET'])
def get_data():
    """Get data endpoint"""
    with tracer.start_as_current_span("get_data") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "get_data")
        
        structured_logger.info("Fetching data from Service C", trace_id=request.trace_id)
        
        # Simulate database query
        time.sleep(0.05)
        
        response = {
            'service': 'service-c',
            'message': 'Data retrieved from Service C',
            'trace_id': request.trace_id,
            'timestamp': datetime.utcnow().isoformat() + 'Z',
            'data': {
                'products': [
                    {'id': 1, 'name': 'Widget A', 'price': 19.99},
                    {'id': 2, 'name': 'Widget B', 'price': 29.99},
                    {'id': 3, 'name': 'Widget C', 'price': 39.99}
                ],
                'total_count': 3,
                'currency': 'USD'
            }
        }
        
        structured_logger.info(
            "Data retrieved successfully",
            trace_id=request.trace_id,
            record_count=response['data']['total_count']
        )
        
        return jsonify(response)

@app.route('/api/error', methods=['GET'])
def simulate_error():
    """Simulate error endpoint"""
    with tracer.start_as_current_span("simulate_error") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "simulate_error")
        
        structured_logger.warning("Simulating error in Service C", trace_id=request.trace_id)
        
        # Simulate different types of errors
        import random
        error_types = ['database_connection', 'timeout', 'validation_failed']
        error_type = random.choice(error_types)
        
        error = Exception(f"Simulated {error_type} error in Service C")
        
        structured_logger.error(
            "Simulated error occurred",
            trace_id=request.trace_id,
            error_type=error_type,
            error=str(error)
        )
        
        span.record_exception(error)
        
        return jsonify({
            'service': 'service-c',
            'error': error_type,
            'message': str(error),
            'trace_id': request.trace_id,
            'timestamp': datetime.utcnow().isoformat() + 'Z'
        }), 500

@app.route('/api/timeout', methods=['GET'])
def simulate_timeout():
    """Simulate timeout error endpoint"""
    with tracer.start_as_current_span("simulate_timeout") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "simulate_timeout")
        
        structured_logger.warning("Simulating timeout in Service C", trace_id=request.trace_id)
        
        # Simulate a long operation that would timeout
        timeout_duration = 20  # 20 seconds
        
        try:
            # Simulate waiting for external service
            time.sleep(timeout_duration)
            
            # This shouldn't be reached due to typical request timeouts
            return jsonify({
                'service': 'service-c',
                'message': 'Operation completed unexpectedly',
                'trace_id': request.trace_id,
                'timestamp': datetime.utcnow().isoformat() + 'Z'
            })
            
        except Exception as e:
            error = Exception(f"Operation timed out after {timeout_duration} seconds")
            
            structured_logger.error(
                "Timeout error occurred",
                trace_id=request.trace_id,
                timeout_duration=timeout_duration,
                error=str(error)
            )
            
            span.record_exception(error)
            
            return jsonify({
                'service': 'service-c',
                'error': 'timeout',
                'message': str(error),
                'trace_id': request.trace_id,
                'timestamp': datetime.utcnow().isoformat() + 'Z'
            }), 408

@app.route('/api/call-service-a-error', methods=['GET'])
def call_service_a_error():
    """Call Service A but expect it to fail - demonstrates error propagation"""
    with tracer.start_as_current_span("call_service_a_error_scenario") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "call_service_a_error_scenario")
        
        structured_logger.info(
            "Service C calling Service A (expecting failure)",
            trace_id=request.trace_id
        )
        
        try:
            # Try to call a non-existent endpoint on Service A
            service_a_response = make_request_to_service_a(request.trace_id, '/api/nonexistent')
            
            # This shouldn't happen
            response = {
                'service': 'service-c',
                'message': 'Service C called Service A unexpectedly successfully',
                'trace_id': request.trace_id,
                'timestamp': datetime.utcnow().isoformat() + 'Z',
                'service_a_response': service_a_response
            }
            
            return jsonify(response)
            
        except Exception as e:
            error_response = {
                'service': 'service-c',
                'error': 'Failed to call Service A (expected)',
                'message': str(e),
                'trace_id': request.trace_id,
                'timestamp': datetime.utcnow().isoformat() + 'Z',
                'scenario': 'error_propagation_test'
            }
            
            structured_logger.error(
                "Expected failure when calling Service A",
                trace_id=request.trace_id,
                error=str(e)
            )
            
            span.record_exception(e)
            return jsonify(error_response), 502

@app.route('/api/auth-error', methods=['GET'])
def simulate_auth_error():
    """Simulate authentication/authorization error"""
    with tracer.start_as_current_span("simulate_auth_error") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "simulate_auth_error")
        
        structured_logger.warning("Simulating auth error in Service C", trace_id=request.trace_id)
        
        error = Exception("Authentication failed: Invalid JWT token")
        
        structured_logger.error(
            "Authentication error occurred",
            trace_id=request.trace_id,
            error_type="authentication_failed",
            error=str(error)
        )
        
        span.record_exception(error)
        
        return jsonify({
            'service': 'service-c',
            'error': 'authentication_failed',
            'message': str(error),
            'trace_id': request.trace_id,
            'timestamp': datetime.utcnow().isoformat() + 'Z'
        }), 401

@app.route('/api/rate-limit-error', methods=['GET'])
def simulate_rate_limit_error():
    """Simulate rate limiting error"""
    with tracer.start_as_current_span("simulate_rate_limit_error") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "simulate_rate_limit_error")
        
        structured_logger.warning("Simulating rate limit error in Service C", trace_id=request.trace_id)
        
        error = Exception("Rate limit exceeded: Maximum 100 requests per minute")
        
        structured_logger.error(
            "Rate limit error occurred",
            trace_id=request.trace_id,
            error_type="rate_limit_exceeded",
            error=str(error),
            current_requests=150,
            limit=100
        )
        
        span.record_exception(error)
        
        return jsonify({
            'service': 'service-c',
            'error': 'rate_limit_exceeded',
            'message': str(error),
            'trace_id': request.trace_id,
            'timestamp': datetime.utcnow().isoformat() + 'Z',
            'retry_after': 60
        }), 429

@app.route('/api/dependency-error', methods=['GET'])
def simulate_dependency_error():
    """Simulate external dependency failure"""
    with tracer.start_as_current_span("simulate_dependency_error") as span:
        span.set_attribute("trace.id", request.trace_id)
        span.set_attribute("operation", "simulate_dependency_error")
        
        structured_logger.warning("Simulating dependency error in Service C", trace_id=request.trace_id)
        
        # Simulate calling an external service that fails
        dependency_name = "external-payment-api"
        error = Exception(f"External dependency '{dependency_name}' is unavailable")
        
        structured_logger.error(
            "External dependency error occurred",
            trace_id=request.trace_id,
            error_type="dependency_unavailable",
            dependency=dependency_name,
            error=str(error)
        )
        
        span.record_exception(error)
        
        return jsonify({
            'service': 'service-c',
            'error': 'dependency_unavailable',
            'message': str(error),
            'dependency': dependency_name,
            'trace_id': request.trace_id,
            'timestamp': datetime.utcnow().isoformat() + 'Z'
        }), 503

@app.errorhandler(Exception)
def handle_exception(e):
    """Global error handler"""
    trace_id = getattr(request, 'trace_id', 'unknown')
    
    structured_logger.error(
        "Unhandled error",
        trace_id=trace_id,
        error=str(e),
        path=request.path,
        method=request.method
    )
    
    # Add exception to current span
    span = trace.get_current_span()
    if span.is_recording():
        span.record_exception(e)
    
    return jsonify({
        'service': 'service-c',
        'error': 'Internal server error',
        'trace_id': trace_id,
        'timestamp': datetime.utcnow().isoformat() + 'Z'
    }), 500

if __name__ == '__main__':
    port = int(os.getenv('PORT', 5000))
    structured_logger.info(
        f"Service C starting on port {port}",
        port=port,
        environment=os.getenv('FLASK_ENV', 'production')
    )
    
    app.run(host='0.0.0.0', port=port, debug=False)