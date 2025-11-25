const express = require('express');
const { v4: uuidv4 } = require('uuid');
const winston = require('winston');
const { NodeSDK } = require('@opentelemetry/sdk-node');
const { getNodeAutoInstrumentations } = require('@opentelemetry/auto-instrumentations-node');
const { OTLPTraceExporter } = require('@opentelemetry/exporter-trace-otlp-grpc');
const opentelemetry = require('@opentelemetry/api');

// Initialize OpenTelemetry with custom configuration
const otlpExporter = new OTLPTraceExporter({
  url: process.env.OTEL_EXPORTER_OTLP_ENDPOINT || 'http://otel-collector:4317',
});

const sdk = new NodeSDK({
  traceExporter: otlpExporter,
  instrumentations: [
    getNodeAutoInstrumentations({
      // Customize auto-instrumentation behavior
      '@opentelemetry/instrumentation-http': {
        enabled: true,
      },
      '@opentelemetry/instrumentation-express': {
        enabled: true,
      }
    })
  ],
});

sdk.start();

// Note: The NodeSDK automatically sets up standard propagators

// Configure Winston logger
const logger = winston.createLogger({
  level: 'info',
  format: winston.format.combine(
    winston.format.timestamp(),
    winston.format.errors({ stack: true }),
    winston.format.json(),
    winston.format.printf(({ timestamp, level, message, traceId, ...meta }) => {
      return JSON.stringify({
        timestamp,
        level,
        message,
        trace_id: traceId,
        service: 'service-b',
        ...meta
      });
    })
  ),
  transports: [
    new winston.transports.Console()
  ],
});

const app = express();
app.use(express.json());

// Tracing middleware - Simplified to work with auto-instrumentation
const tracingMiddleware = (req, res, next) => {
  // Log incoming headers for debugging
  logger.info('Incoming headers', { 
    headers: req.headers,
    path: req.path 
  });
  
  // Extract or generate trace ID (case insensitive)
  let traceId = req.headers['x-trace-id'] || req.headers['X-Trace-ID'] || 
                req.headers['x-amzn-trace-id'] || req.headers['X-Amzn-Trace-Id'];
  
  if (!traceId) {
    // This should not happen in production if properly configured
    traceId = `fallback-${Date.now()}-${uuidv4().substring(0, 8)}`;
    logger.warn('Missing trace ID from upstream - generated fallback', { 
      traceId,
      headers: req.headers,
      path: req.path 
    });
  }

  // Add trace ID to request context
  req.traceId = traceId;
  
  // Add trace ID to response headers
  res.setHeader('X-Trace-ID', traceId);
  
  // Let auto-instrumentation handle OpenTelemetry context
  // Just add our custom attributes to the current span if it exists
  const span = opentelemetry.trace.getActiveSpan();
  if (span) {
    // Note: req.route is not available yet in middleware, will be set in route handlers
    span.setAttributes({
      'trace.id': traceId,
      'http.method': req.method,
      'http.url': req.originalUrl,
      'service.name': 'service-b'
    });
    logger.info('Added attributes to active span');
  }
  
  next();
};



app.use(tracingMiddleware);

// Health endpoint
app.get('/health', (req, res) => {
  // Update the auto-instrumentation span name
  const currentSpan = opentelemetry.trace.getActiveSpan();
  if (currentSpan) {
    currentSpan.updateName('GET /health');
    currentSpan.setAttributes({
      'http.route': '/health',
      'operation': 'health_check'
    });
  }
  
  const tracer = opentelemetry.trace.getTracer('service-b');
  const span = tracer.startSpan('health_check');
  
  try {
    span.setAttributes({
      'trace.id': req.traceId,
      'operation': 'health_check'
    });

    const response = {
      service: 'service-b',
      message: 'Service B is healthy',
      trace_id: req.traceId,
      timestamp: new Date().toISOString()
    };

    logger.info('Health check completed', { traceId: req.traceId });
    res.json(response);
  } catch (error) {
    logger.error('Health check failed', { traceId: req.traceId, error: error.message });
    res.status(500).json({ error: 'Health check failed' });
  } finally {
    span.end();
  }
});

// Process endpoint
app.get('/api/process', (req, res) => {
  // Update the auto-instrumentation span name
  const currentSpan = opentelemetry.trace.getActiveSpan();
  if (currentSpan) {
    currentSpan.updateName('GET /api/process');
    currentSpan.setAttributes({
      'http.route': '/api/process',
      'operation': 'process_request'
    });
  }
  
  const tracer = opentelemetry.trace.getTracer('service-b');
  const span = tracer.startSpan('process_request');
  
  try {
    span.setAttributes({
      'trace.id': req.traceId,
      'operation': 'process_request'
    });

    logger.info('Processing request in Service B', { traceId: req.traceId });
    
    // Simulate some processing time
    const processingTime = Math.random() * 200 + 100; // 100-300ms
    
    setTimeout(() => {
      const response = {
        service: 'service-b',
        message: 'Request processed by Service B (Node.js)',
        trace_id: req.traceId,
        timestamp: new Date().toISOString(),
        processing_time_ms: Math.round(processingTime),
        data: {
          processed_items: Math.floor(Math.random() * 100) + 1,
          status: 'success'
        }
      };

      logger.info('Request processed successfully', { 
        traceId: req.traceId, 
        processingTime: Math.round(processingTime),
        processedItems: response.data.processed_items
      });
      
      res.json(response);
      span.end();
    }, processingTime);

  } catch (error) {
    logger.error('Error processing request', { 
      traceId: req.traceId, 
      error: error.message,
      stack: error.stack
    });
    res.status(500).json({ 
      service: 'service-b',
      error: 'Processing failed',
      trace_id: req.traceId
    });
    span.recordException(error);
    span.end();
  }
});

// Data endpoint
app.get('/api/data', (req, res) => {
  // Update the auto-instrumentation span name
  const currentSpan = opentelemetry.trace.getActiveSpan();
  if (currentSpan) {
    currentSpan.updateName('GET /api/data');
    currentSpan.setAttributes({
      'http.route': '/api/data',
      'operation': 'get_data'
    });
  }
  
  const tracer = opentelemetry.trace.getTracer('service-b');
  const span = tracer.startSpan('get_data');
  
  try {
    span.setAttributes({
      'trace.id': req.traceId,
      'operation': 'get_data'
    });

    logger.info('Fetching data from Service B', { traceId: req.traceId });
    
    const response = {
      service: 'service-b',
      message: 'Data retrieved from Service B',
      trace_id: req.traceId,
      timestamp: new Date().toISOString(),
      data: {
        users: [
          { id: 1, name: 'John Doe', email: 'john@example.com' },
          { id: 2, name: 'Jane Smith', email: 'jane@example.com' }
        ],
        total_count: 2,
        page: 1
      }
    };

    logger.info('Data retrieved successfully', { 
      traceId: req.traceId,
      recordCount: response.data.total_count
    });
    
    res.json(response);
  } catch (error) {
    logger.error('Error fetching data', { 
      traceId: req.traceId, 
      error: error.message 
    });
    res.status(500).json({ 
      service: 'service-b',
      error: 'Data fetch failed',
      trace_id: req.traceId
    });
    span.recordException(error);
  } finally {
    span.end();
  }
});

// Error simulation endpoint
app.get('/api/error', (req, res) => {
  const tracer = opentelemetry.trace.getTracer('service-b');
  const span = tracer.startSpan('simulate_error');
  
  try {
    span.setAttributes({
      'trace.id': req.traceId,
      'operation': 'simulate_error'
    });

    logger.warn('Simulating error in Service B', { traceId: req.traceId });
    
    // Simulate random error
    const errorTypes = ['database_error', 'network_timeout', 'validation_error'];
    const errorType = errorTypes[Math.floor(Math.random() * errorTypes.length)];
    
    const error = new Error(`Simulated ${errorType} in Service B`);
    
    logger.error('Simulated error occurred', { 
      traceId: req.traceId, 
      errorType,
      error: error.message
    });
    
    span.recordException(error);
    res.status(500).json({ 
      service: 'service-b',
      error: errorType,
      message: error.message,
      trace_id: req.traceId,
      timestamp: new Date().toISOString()
    });
  } finally {
    span.end();
  }
});

// Timeout simulation endpoint
app.get('/api/timeout', (req, res) => {
  const tracer = opentelemetry.trace.getTracer('service-b');
  const span = tracer.startSpan('simulate_timeout');
  
  try {
    span.setAttributes({
      'trace.id': req.traceId,
      'operation': 'simulate_timeout'
    });

    logger.warn('Simulating timeout in Service B', { traceId: req.traceId });
    
    // Simulate a long-running operation that times out
    const timeoutDuration = 15000; // 15 seconds
    
    setTimeout(() => {
      logger.error('Operation timed out in Service B', { 
        traceId: req.traceId,
        timeoutDuration 
      });
      
      const error = new Error('Request timeout after 15 seconds');
      span.recordException(error);
      
      if (!res.headersSent) {
        res.status(408).json({
          service: 'service-b',
          error: 'timeout',
          message: 'Request timeout after 15 seconds',
          trace_id: req.traceId,
          timestamp: new Date().toISOString()
        });
      }
    }, timeoutDuration);
    
    // This response will never be sent due to timeout
  } finally {
    span.end();
  }
});

// Database connection failure simulation
app.get('/api/db-error', (req, res) => {
  const tracer = opentelemetry.trace.getTracer('service-b');
  const span = tracer.startSpan('simulate_db_error');
  
  try {
    span.setAttributes({
      'trace.id': req.traceId,
      'operation': 'simulate_db_error'
    });

    logger.warn('Simulating database error in Service B', { traceId: req.traceId });
    
    // Simulate database connection failure
    setTimeout(() => {
      const error = new Error('Database connection failed: Connection refused to localhost:5432');
      
      logger.error('Database connection error occurred', { 
        traceId: req.traceId,
        error: error.message,
        database: 'postgresql'
      });
      
      span.recordException(error);
      res.status(503).json({
        service: 'service-b',
        error: 'database_connection_failed',
        message: error.message,
        trace_id: req.traceId,
        timestamp: new Date().toISOString(),
        retry_after: 30
      });
    }, 100);
    
  } finally {
    span.end();
  }
});

// Memory exhaustion simulation
app.get('/api/memory-error', (req, res) => {
  const tracer = opentelemetry.trace.getTracer('service-b');
  const span = tracer.startSpan('simulate_memory_error');
  
  try {
    span.setAttributes({
      'trace.id': req.traceId,
      'operation': 'simulate_memory_error'
    });

    logger.warn('Simulating memory exhaustion in Service B', { traceId: req.traceId });
    
    const error = new Error('JavaScript heap out of memory');
    
    logger.error('Memory exhaustion error occurred', { 
      traceId: req.traceId,
      error: error.message,
      memoryUsage: process.memoryUsage()
    });
    
    span.recordException(error);
    res.status(507).json({
      service: 'service-b',
      error: 'insufficient_storage',
      message: error.message,
      trace_id: req.traceId,
      timestamp: new Date().toISOString()
    });
    
  } finally {
    span.end();
  }
});

// Circular call endpoints for Service B
// Call Service A from Service B
app.get('/api/call-service-a', async (req, res) => {
  // Update the auto-instrumentation span name
  const currentSpan = opentelemetry.trace.getActiveSpan();
  if (currentSpan) {
    currentSpan.updateName('GET /api/call-service-a');
    currentSpan.setAttributes({
      'http.route': '/api/call-service-a',
      'operation': 'call_service_a'
    });
  }
  
  const tracer = opentelemetry.trace.getTracer('service-b');
  
  // Don't create a manual span at all - let auto-instrumentation handle it
  try {
    logger.info('Service B calling Service A', { traceId: req.traceId });
    
    const serviceAUrl = process.env.SERVICE_A_URL || 'http://service-a:8080';
    const url = `${serviceAUrl}/api/process`;
    
    // Prepare headers with trace propagation
    const headers = {
      'X-Trace-ID': req.traceId,
      'Content-Type': 'application/json'
    };
    
    // Inject OpenTelemetry context into headers
    opentelemetry.propagation.inject(opentelemetry.context.active(), headers);
    
    // Log outgoing headers for debugging
    logger.info('Outgoing headers to Service A', { 
      traceId: req.traceId,
      headers: headers
    });
    
    logger.info(`Making request to Service A: ${url}`, { traceId: req.traceId });
    
    const response = await fetch(url, { 
      method: 'GET',
      headers: headers,
      timeout: 10000 
    });
    
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }
    
    const serviceAResponse = await response.json();
    
    logger.info('Received response from Service A', { 
      traceId: req.traceId,
      statusCode: response.status,
      responseService: serviceAResponse.service
    });
    
    const result = {
      service: 'service-b',
      message: 'Service B successfully called Service A',
      trace_id: req.traceId,
      timestamp: new Date().toISOString(),
      service_a_response: serviceAResponse
    };
    
    logger.info('Successfully called Service A from Service B', { traceId: req.traceId });
    res.json(result);
    
  } catch (error) {
    logger.error('Failed to call Service A from Service B', { 
      traceId: req.traceId,
      error: error.message
    });

    const currentSpan = opentelemetry.trace.getActiveSpan();
    if (currentSpan) {
      currentSpan.recordException(error);
    }
    
    res.status(502).json({
      service: 'service-b',
      error: 'Failed to call Service A',
      message: error.message,
      trace_id: req.traceId,
      timestamp: new Date().toISOString()
    });
  }
});

// Call Service C from Service B
app.get('/api/call-service-c', async (req, res) => {
  // Update the auto-instrumentation span name
  const currentSpan = opentelemetry.trace.getActiveSpan();
  if (currentSpan) {
    currentSpan.updateName('GET /api/call-service-c');
    currentSpan.setAttributes({
      'http.route': '/api/call-service-c',
      'operation': 'call_service_c'
    });
  }
  
  // Let auto-instrumentation handle all span creation
  try {
    logger.info('Service B calling Service C', { traceId: req.traceId });
    
    const serviceCUrl = process.env.SERVICE_C_URL || 'http://service-c:5000';
    const url = `${serviceCUrl}/api/process`;
    
    // Prepare headers with trace propagation
    const headers = {
      'X-Trace-ID': req.traceId,
      'Content-Type': 'application/json'
    };
    
    // Inject OpenTelemetry context into headers
    opentelemetry.propagation.inject(opentelemetry.context.active(), headers);
    
    // Log outgoing headers for debugging
    logger.info('Outgoing headers to Service C', { 
      traceId: req.traceId,
      headers: headers
    });
    
    logger.info(`Making request to Service C: ${url}`, { traceId: req.traceId });
    
    const response = await fetch(url, { 
      method: 'GET',
      headers: headers,
      timeout: 10000 
    });
    
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }
    
    const serviceCResponse = await response.json();
    
    logger.info('Received response from Service C', { 
      traceId: req.traceId,
      statusCode: response.status,
      responseService: serviceCResponse.service
    });
    
    const result = {
      service: 'service-b',
      message: 'Service B successfully called Service C',
      trace_id: req.traceId,
      timestamp: new Date().toISOString(),
      service_c_response: serviceCResponse
    };
    
    logger.info('Successfully called Service C from Service B', { traceId: req.traceId });
    res.json(result);
    
  } catch (error) {
    logger.error('Failed to call Service C from Service B', { 
      traceId: req.traceId,
      error: error.message
    });
    
    const currentSpan = opentelemetry.trace.getActiveSpan();
    if (currentSpan) {
      currentSpan.recordException(error);
    }
    
    res.status(502).json({
      service: 'service-b',
      error: 'Failed to call Service C',
      message: error.message,
      trace_id: req.traceId,
      timestamp: new Date().toISOString()
    });
  }
});

// Error handling middleware
app.use((error, req, res, next) => {
  const traceId = req.traceId || 'unknown';
  
  logger.error('Unhandled error', { 
    traceId, 
    error: error.message,
    stack: error.stack,
    path: req.path,
    method: req.method
  });
  
  res.status(500).json({
    service: 'service-b',
    error: 'Internal server error',
    trace_id: traceId,
    timestamp: new Date().toISOString()
  });
});

const PORT = process.env.PORT || 3000;

app.listen(PORT, () => {
  logger.info(`Service B starting on port ${PORT}`, { 
    port: PORT,
    environment: process.env.NODE_ENV || 'development'
  });
});

// Graceful shutdown
process.on('SIGINT', () => {
  logger.info('Service B shutting down gracefully');
  sdk.shutdown()
    .then(() => {
      logger.info('OpenTelemetry terminated');
      process.exit(0);
    })
    .catch((error) => {
      logger.error('Error terminating OpenTelemetry', { error: error.message });
      process.exit(1);
    });
});