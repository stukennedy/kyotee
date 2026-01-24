import { FlightSimulator } from './flight-simulator';

export { FlightSimulator };

export interface Env {
  FLIGHT_SIM: DurableObjectNamespace;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    
    // Serve static assets from public/
    if (url.pathname === '/' || url.pathname === '/index.html') {
      return env.ASSETS?.fetch(request) ?? new Response('Not found', { status: 404 });
    }

    // API routes go to the Durable Object
    if (url.pathname.startsWith('/api/')) {
      // Use a single flight simulator instance for shared state
      const id = env.FLIGHT_SIM.idFromName('main-flight');
      const stub = env.FLIGHT_SIM.get(id);
      return stub.fetch(request);
    }

    // Let assets binding handle other static files
    return env.ASSETS?.fetch(request) ?? new Response('Not found', { status: 404 });
  },
};
