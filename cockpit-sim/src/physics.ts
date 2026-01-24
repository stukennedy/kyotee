export interface FlightState {
  // Position & orientation
  altitude: number;         // feet
  heading: number;          // degrees
  pitch: number;            // degrees
  roll: number;             // degrees
  
  // Velocities
  airspeed: number;         // knots indicated
  groundSpeed: number;      // knots
  verticalSpeed: number;    // feet per minute
  mach: number;
  
  // Engine
  throttle: number;         // 0-1 current
  n1Left: number;           // percent
  n1Right: number;
  egtLeft: number;          // celsius
  egtRight: number;
  fuelFlow: number;         // lbs/hr
  
  // Atmosphere
  outsideAirTemp: number;   // celsius
  windSpeed: number;        // knots
  windDirection: number;    // from degrees
  
  // Control inputs
  targetThrottle: number;
  targetPitch: number;
  targetHeading: number;
  
  // Autopilot
  autopilotEngaged: boolean;
  autothrottleEngaged: boolean;
}

export class FlightPhysics {
  // Aircraft performance constants (loosely based on 737-800)
  private readonly maxThrust = 52000;     // lbs total
  private readonly weight = 140000;        // lbs
  private readonly wingArea = 1341;        // sq ft
  private readonly maxN1 = 104;            // percent
  private readonly idleN1 = 21;            // percent
  
  // Response rates
  private readonly throttleRate = 0.3;     // per second
  private readonly pitchRate = 3;          // degrees per second
  private readonly rollRate = 15;          // degrees per second
  private readonly headingRate = 3;        // degrees per second
  
  update(state: FlightState, dt: number): FlightState {
    const newState = { ...state };
    
    // Add small random variations for realism
    const turbulence = (Math.random() - 0.5) * 0.5;
    
    // --- Throttle/Engine response ---
    if (state.autothrottleEngaged) {
      const throttleDiff = state.targetThrottle - state.throttle;
      newState.throttle = state.throttle + Math.sign(throttleDiff) * 
        Math.min(Math.abs(throttleDiff), this.throttleRate * dt);
    }
    
    // N1 follows throttle with lag
    const targetN1 = this.idleN1 + (this.maxN1 - this.idleN1) * newState.throttle;
    const n1Diff = targetN1 - state.n1Left;
    newState.n1Left = state.n1Left + n1Diff * dt * 2 + turbulence * 0.1;
    newState.n1Right = state.n1Right + (targetN1 - state.n1Right) * dt * 2 - turbulence * 0.1;
    
    // EGT correlates with N1
    newState.egtLeft = 400 + (state.n1Left / 100) * 450 + turbulence * 2;
    newState.egtRight = 400 + (state.n1Right / 100) * 450 - turbulence * 2;
    
    // Fuel flow based on throttle
    newState.fuelFlow = 1200 + newState.throttle * 5000 + Math.random() * 50;
    
    // --- Autopilot pitch control ---
    if (state.autopilotEngaged) {
      const pitchDiff = state.targetPitch - state.pitch;
      newState.pitch = state.pitch + Math.sign(pitchDiff) * 
        Math.min(Math.abs(pitchDiff), this.pitchRate * dt);
    }
    newState.pitch += turbulence * 0.1;
    newState.pitch = Math.max(-15, Math.min(20, newState.pitch));
    
    // --- Autopilot heading control ---
    if (state.autopilotEngaged) {
      let headingDiff = state.targetHeading - state.heading;
      // Normalize to -180 to 180
      while (headingDiff > 180) headingDiff -= 360;
      while (headingDiff < -180) headingDiff += 360;
      
      // Bank into turn
      const targetRoll = Math.sign(headingDiff) * Math.min(Math.abs(headingDiff), 25);
      const rollDiff = targetRoll - state.roll;
      newState.roll = state.roll + Math.sign(rollDiff) * 
        Math.min(Math.abs(rollDiff), this.rollRate * dt);
      
      // Heading changes based on roll
      const turnRate = (newState.roll / 25) * this.headingRate;
      newState.heading = (state.heading + turnRate * dt + 360) % 360;
    }
    newState.roll += turbulence * 0.2;
    newState.roll = Math.max(-35, Math.min(35, newState.roll));
    
    // --- Airspeed calculation ---
    // Thrust vs drag simplified model
    const thrust = (newState.n1Left + newState.n1Right) / 200 * this.maxThrust;
    const altitude = state.altitude;
    const airDensityRatio = Math.exp(-altitude / 30000); // Simplified
    
    // Drag increases with speed squared
    const dragCoeff = 0.025 + Math.pow(state.pitch / 100, 2) * 0.1;
    const drag = 0.5 * airDensityRatio * Math.pow(state.airspeed * 1.687, 2) * 
      this.wingArea * dragCoeff / 100000;
    
    const netForce = thrust - drag * 1000;
    const acceleration = netForce / this.weight * 32.2; // ft/s^2
    
    // Update airspeed (simplified, knots)
    newState.airspeed = state.airspeed + acceleration * dt * 0.1;
    newState.airspeed = Math.max(150, Math.min(550, newState.airspeed));
    newState.airspeed += turbulence;
    
    // Mach number (simplified)
    const tempKelvin = 273 + state.outsideAirTemp;
    const speedOfSound = 38.94 * Math.sqrt(tempKelvin); // knots
    newState.mach = newState.airspeed / speedOfSound;
    
    // Ground speed with wind
    const windComponent = state.windSpeed * 
      Math.cos((state.windDirection - state.heading) * Math.PI / 180);
    newState.groundSpeed = newState.airspeed - windComponent;
    
    // --- Vertical speed & altitude ---
    // Pitch affects vertical speed
    const climbRate = newState.airspeed * Math.sin(newState.pitch * Math.PI / 180) * 60; // fpm
    const targetVS = climbRate + turbulence * 50;
    newState.verticalSpeed = state.verticalSpeed + (targetVS - state.verticalSpeed) * dt * 0.5;
    
    // Update altitude
    newState.altitude = state.altitude + newState.verticalSpeed * dt / 60;
    newState.altitude = Math.max(0, Math.min(45000, newState.altitude));
    
    // Update temperature based on altitude
    newState.outsideAirTemp = 15 - (newState.altitude / 1000) * 2;
    
    // Slight wind variations
    newState.windSpeed = state.windSpeed + (Math.random() - 0.5) * dt;
    newState.windSpeed = Math.max(0, Math.min(150, newState.windSpeed));
    newState.windDirection = (state.windDirection + (Math.random() - 0.5) * dt + 360) % 360;
    
    return newState;
  }
}
