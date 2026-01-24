import { DurableObject } from 'cloudflare:workers';
import { FlightState, FlightPhysics } from './physics';

export interface Env {
  FLIGHT_SIM: DurableObjectNamespace;
}

export class FlightSimulator extends DurableObject<Env> {
  private state: FlightState;
  private physics: FlightPhysics;
  private clients: Map<string, WritableStreamDefaultWriter<Uint8Array>>;
  private simulationInterval: number | null = null;
  private lastUpdate: number;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.clients = new Map();
    this.lastUpdate = Date.now();
    
    // Initialize flight state
    this.state = {
      // Position & orientation
      altitude: 35000,        // feet
      heading: 270,           // degrees (west)
      pitch: 2,               // degrees nose up
      roll: 0,                // degrees
      
      // Velocities
      airspeed: 450,          // knots indicated
      groundSpeed: 480,       // knots
      verticalSpeed: 0,       // feet per minute
      mach: 0.78,
      
      // Engine
      throttle: 0.72,         // 0-1
      n1Left: 87,             // percent
      n1Right: 87,
      egtLeft: 720,           // celsius
      egtRight: 718,
      fuelFlow: 4200,         // lbs/hr total
      
      // Atmosphere
      outsideAirTemp: -56,    // celsius at FL350
      windSpeed: 45,          // knots
      windDirection: 290,     // from
      
      // Control inputs (from users)
      targetThrottle: 0.72,
      targetPitch: 2,
      targetHeading: 270,
      
      // Autopilot
      autopilotEngaged: true,
      autothrottleEngaged: true,
    };
    
    this.physics = new FlightPhysics();
    
    // Restore state from storage if available
    this.ctx.blockConcurrencyWhile(async () => {
      const stored = await this.ctx.storage.get<FlightState>('flightState');
      if (stored) {
        this.state = stored;
      }
    });
  }

  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    
    switch (url.pathname) {
      case '/api/stream':
        return this.handleSSE(request);
      
      case '/api/control':
        if (request.method === 'POST') {
          return this.handleControl(request);
        }
        return new Response('Method not allowed', { status: 405 });
      
      case '/api/state':
        return new Response(JSON.stringify(this.state), {
          headers: { 'Content-Type': 'application/json' },
        });
      
      default:
        return new Response('Not found', { status: 404 });
    }
  }

  private handleSSE(request: Request): Response {
    const clientId = crypto.randomUUID();
    
    const { readable, writable } = new TransformStream<Uint8Array, Uint8Array>();
    const writer = writable.getWriter();
    
    this.clients.set(clientId, writer);
    
    // Start simulation if not running
    if (this.simulationInterval === null) {
      this.startSimulation();
    }
    
    // Send initial state
    const initialData = `data: ${JSON.stringify(this.state)}\n\n`;
    writer.write(new TextEncoder().encode(initialData));
    
    // Clean up on disconnect
    request.signal.addEventListener('abort', () => {
      this.clients.delete(clientId);
      writer.close().catch(() => {});
      
      if (this.clients.size === 0 && this.simulationInterval !== null) {
        this.ctx.storage.deleteAlarm();
        this.simulationInterval = null;
      }
    });
    
    return new Response(readable, {
      headers: {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'Connection': 'keep-alive',
        'Access-Control-Allow-Origin': '*',
      },
    });
  }

  private async handleControl(request: Request): Promise<Response> {
    try {
      const body = await request.json() as Partial<{
        throttle: number;
        pitch: number;
        heading: number;
        autopilot: boolean;
        autothrottle: boolean;
      }>;
      
      // Apply control inputs
      if (typeof body.throttle === 'number') {
        this.state.targetThrottle = Math.max(0, Math.min(1, body.throttle));
      }
      if (typeof body.pitch === 'number') {
        this.state.targetPitch = Math.max(-15, Math.min(20, body.pitch));
      }
      if (typeof body.heading === 'number') {
        this.state.targetHeading = ((body.heading % 360) + 360) % 360;
      }
      if (typeof body.autopilot === 'boolean') {
        this.state.autopilotEngaged = body.autopilot;
      }
      if (typeof body.autothrottle === 'boolean') {
        this.state.autothrottleEngaged = body.autothrottle;
      }
      
      return new Response(JSON.stringify({ success: true, state: this.state }), {
        headers: { 
          'Content-Type': 'application/json',
          'Access-Control-Allow-Origin': '*',
        },
      });
    } catch (e) {
      return new Response(JSON.stringify({ error: 'Invalid request' }), {
        status: 400,
        headers: { 'Content-Type': 'application/json' },
      });
    }
  }

  private startSimulation(): void {
    // Use alarm for simulation ticks
    this.simulationInterval = 1;
    this.ctx.storage.setAlarm(Date.now() + 100); // 10Hz update
  }

  async alarm(): Promise<void> {
    if (this.clients.size === 0) {
      this.simulationInterval = null;
      return;
    }
    
    const now = Date.now();
    const deltaTime = (now - this.lastUpdate) / 1000; // seconds
    this.lastUpdate = now;
    
    // Update physics
    this.state = this.physics.update(this.state, deltaTime);
    
    // Save state periodically
    await this.ctx.storage.put('flightState', this.state);
    
    // Broadcast to all clients
    const data = `data: ${JSON.stringify(this.state)}\n\n`;
    const encoded = new TextEncoder().encode(data);
    
    const deadClients: string[] = [];
    for (const [clientId, writer] of this.clients) {
      try {
        await writer.write(encoded);
      } catch {
        deadClients.push(clientId);
      }
    }
    
    // Clean up dead clients
    for (const clientId of deadClients) {
      this.clients.delete(clientId);
    }
    
    // Schedule next tick
    if (this.clients.size > 0) {
      this.ctx.storage.setAlarm(Date.now() + 100);
    } else {
      this.simulationInterval = null;
    }
  }
}
