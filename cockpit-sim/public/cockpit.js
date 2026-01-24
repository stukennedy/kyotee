// Glass Cockpit Simulator - Client

class CockpitDisplay {
  constructor() {
    this.state = null;
    this.eventSource = null;
    this.reconnectDelay = 1000;
    this.maxReconnectDelay = 30000;
    
    this.initControls();
    this.connectSSE();
    this.startClock();
  }
  
  connectSSE() {
    if (this.eventSource) {
      this.eventSource.close();
    }
    
    this.eventSource = new EventSource('/api/stream');
    
    this.eventSource.onopen = () => {
      console.log('SSE connected');
      this.reconnectDelay = 1000;
      this.updateConnectionStatus(true);
    };
    
    this.eventSource.onmessage = (event) => {
      try {
        this.state = JSON.parse(event.data);
        this.updateDisplay();
      } catch (e) {
        console.error('Failed to parse state:', e);
      }
    };
    
    this.eventSource.onerror = () => {
      console.log('SSE error, reconnecting...');
      this.updateConnectionStatus(false);
      this.eventSource.close();
      
      setTimeout(() => {
        this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.maxReconnectDelay);
        this.connectSSE();
      }, this.reconnectDelay);
    };
  }
  
  updateConnectionStatus(connected) {
    const el = document.getElementById('connection-status');
    if (connected) {
      el.textContent = '● CONNECTED';
      el.className = 'connected';
    } else {
      el.textContent = '○ DISCONNECTED';
      el.className = 'disconnected';
    }
  }
  
  initControls() {
    // Throttle
    const throttle = document.getElementById('throttle-control');
    const throttleDisplay = document.getElementById('throttle-display');
    throttle.addEventListener('input', (e) => {
      throttleDisplay.textContent = e.target.value + '%';
      this.sendControl({ throttle: parseFloat(e.target.value) / 100 });
    });
    
    // Pitch
    const pitch = document.getElementById('pitch-control');
    const pitchDisplay = document.getElementById('pitch-display');
    pitch.addEventListener('input', (e) => {
      pitchDisplay.textContent = e.target.value + '°';
      this.sendControl({ pitch: parseFloat(e.target.value) });
    });
    
    // Heading
    const heading = document.getElementById('heading-control');
    const headingDisplay = document.getElementById('heading-display');
    heading.addEventListener('input', (e) => {
      headingDisplay.textContent = e.target.value + '°';
      this.sendControl({ heading: parseFloat(e.target.value) });
    });
    
    // Autopilot toggle
    const apToggle = document.getElementById('ap-toggle');
    apToggle.addEventListener('click', () => {
      apToggle.classList.toggle('active');
      this.sendControl({ autopilot: apToggle.classList.contains('active') });
    });
    
    // Autothrottle toggle
    const atToggle = document.getElementById('at-toggle');
    atToggle.addEventListener('click', () => {
      atToggle.classList.toggle('active');
      this.sendControl({ autothrottle: atToggle.classList.contains('active') });
    });
  }
  
  async sendControl(data) {
    try {
      await fetch('/api/control', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data)
      });
    } catch (e) {
      console.error('Failed to send control:', e);
    }
  }
  
  startClock() {
    const updateClock = () => {
      const now = new Date();
      const utc = now.toISOString().substr(11, 8) + 'Z';
      document.getElementById('utc-time').textContent = utc;
    };
    updateClock();
    setInterval(updateClock, 1000);
  }
  
  updateDisplay() {
    if (!this.state) return;
    
    const s = this.state;
    
    // === PFD Updates ===
    
    // Attitude indicator
    const attitudeBall = document.getElementById('attitude-ball');
    const pitchOffset = s.pitch * 4; // pixels per degree
    attitudeBall.setAttribute('transform', `translate(0, ${pitchOffset})`);
    
    // Bank pointer
    const bankPointer = document.getElementById('bank-pointer');
    bankPointer.setAttribute('transform', `rotate(${-s.roll})`);
    
    // Airspeed
    document.getElementById('airspeed-value').textContent = Math.round(s.airspeed);
    
    // Altitude
    document.getElementById('altitude-value').textContent = Math.round(s.altitude).toLocaleString();
    
    // Vertical speed
    const vsPointer = document.getElementById('vs-pointer');
    const vsOffset = Math.max(-60, Math.min(60, -s.verticalSpeed / 50));
    vsPointer.setAttribute('transform', `translate(0, ${vsOffset})`);
    document.getElementById('vs-value').textContent = Math.round(s.verticalSpeed);
    
    // Heading
    document.getElementById('heading-value').textContent = Math.round(s.heading).toString().padStart(3, '0') + '°';
    this.updateHeadingTape(s.heading);
    
    // Mach
    document.getElementById('mach-value').textContent = 'M.' + s.mach.toFixed(2).substr(1);
    
    // Ground speed
    document.getElementById('gs-value').textContent = Math.round(s.groundSpeed);
    
    // === Engine Display ===
    
    // N1 gauges
    const n1Arc = (n1) => (n1 / 104) * 212; // 212 is 270deg arc length
    document.getElementById('n1-left-arc').setAttribute('stroke-dasharray', `${n1Arc(s.n1Left)} 283`);
    document.getElementById('n1-right-arc').setAttribute('stroke-dasharray', `${n1Arc(s.n1Right)} 283`);
    document.getElementById('n1-left-value').textContent = s.n1Left.toFixed(1);
    document.getElementById('n1-right-value').textContent = s.n1Right.toFixed(1);
    
    // Color N1 based on value
    const n1Color = (n1) => n1 > 95 ? '#ff0' : n1 > 100 ? '#f00' : '#0f0';
    document.getElementById('n1-left-arc').setAttribute('stroke', n1Color(s.n1Left));
    document.getElementById('n1-right-arc').setAttribute('stroke', n1Color(s.n1Right));
    
    // EGT
    document.getElementById('egt-left-value').textContent = Math.round(s.egtLeft);
    document.getElementById('egt-right-value').textContent = Math.round(s.egtRight);
    
    // Fuel flow
    document.getElementById('fuel-flow-value').textContent = Math.round(s.fuelFlow).toLocaleString();
    
    // Throttle bar
    document.getElementById('throttle-bar').setAttribute('width', s.throttle * 120);
    document.getElementById('throttle-value').textContent = Math.round(s.throttle * 100) + '%';
    
    // === Nav Display ===
    
    // Compass rose
    this.updateCompassRose(s.heading);
    
    // Heading bug
    const bugAngle = s.targetHeading - s.heading;
    document.getElementById('heading-bug').setAttribute('transform', `translate(200, 200) rotate(${bugAngle})`);
    document.getElementById('nd-heading').textContent = Math.round(s.heading).toString().padStart(3, '0') + '°';
    
    // Wind
    document.getElementById('wind-dir').textContent = Math.round(s.windDirection).toString().padStart(3, '0') + '°';
    document.getElementById('wind-spd').textContent = Math.round(s.windSpeed) + 'KT';
    
    // TAS (approximate)
    const tas = s.airspeed * (1 + s.altitude / 50000);
    document.getElementById('tas-value').textContent = Math.round(tas) + 'KT';
    
    // Temperature
    document.getElementById('sat-value').textContent = Math.round(s.outsideAirTemp) + '°C';
    
    // === Header Status ===
    const apStatus = document.getElementById('ap-status');
    const atStatus = document.getElementById('at-status');
    
    apStatus.className = 'ap-status' + (s.autopilotEngaged ? ' active' : '');
    atStatus.className = 'at-status' + (s.autothrottleEngaged ? ' active' : '');
    
    // Sync control buttons
    document.getElementById('ap-toggle').classList.toggle('active', s.autopilotEngaged);
    document.getElementById('at-toggle').classList.toggle('active', s.autothrottleEngaged);
    
    // Sync sliders to current target values
    document.getElementById('throttle-control').value = s.targetThrottle * 100;
    document.getElementById('pitch-control').value = s.targetPitch;
    document.getElementById('heading-control').value = s.targetHeading;
  }
  
  updateHeadingTape(heading) {
    const tape = document.getElementById('heading-tape');
    tape.innerHTML = '';
    
    const cardinals = { 0: 'N', 90: 'E', 180: 'S', 270: 'W' };
    
    for (let i = -50; i <= 50; i += 10) {
      const deg = ((heading + i) % 360 + 360) % 360;
      const x = i * 2;
      
      const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
      line.setAttribute('x1', x);
      line.setAttribute('y1', -10);
      line.setAttribute('x2', x);
      line.setAttribute('y2', i % 30 === 0 ? 0 : -5);
      line.setAttribute('stroke', '#fff');
      line.setAttribute('stroke-width', '1');
      tape.appendChild(line);
      
      if (i % 30 === 0 || deg % 90 === 0) {
        const text = document.createElementNS('http://www.w3.org/2000/svg', 'text');
        text.setAttribute('x', x);
        text.setAttribute('y', 12);
        text.setAttribute('fill', '#fff');
        text.setAttribute('font-size', '11');
        text.setAttribute('text-anchor', 'middle');
        text.textContent = cardinals[deg] || Math.round(deg / 10);
        tape.appendChild(text);
      }
    }
  }
  
  updateCompassRose(heading) {
    const rose = document.getElementById('compass-rose');
    rose.innerHTML = '';
    
    const cardinals = { 0: 'N', 90: 'E', 180: 'S', 270: 'W' };
    
    // Rotate entire rose opposite to heading
    rose.setAttribute('transform', `translate(200, 200) rotate(${-heading})`);
    
    for (let deg = 0; deg < 360; deg += 10) {
      const angle = deg * Math.PI / 180;
      const inner = deg % 30 === 0 ? 125 : 135;
      const outer = 145;
      
      const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
      line.setAttribute('x1', Math.sin(angle) * inner);
      line.setAttribute('y1', -Math.cos(angle) * inner);
      line.setAttribute('x2', Math.sin(angle) * outer);
      line.setAttribute('y2', -Math.cos(angle) * outer);
      line.setAttribute('stroke', '#fff');
      line.setAttribute('stroke-width', deg % 30 === 0 ? '2' : '1');
      rose.appendChild(line);
      
      if (deg % 30 === 0) {
        const text = document.createElementNS('http://www.w3.org/2000/svg', 'text');
        const textR = 110;
        text.setAttribute('x', Math.sin(angle) * textR);
        text.setAttribute('y', -Math.cos(angle) * textR + 4);
        text.setAttribute('fill', '#fff');
        text.setAttribute('font-size', '12');
        text.setAttribute('text-anchor', 'middle');
        // Rotate text to be readable
        text.setAttribute('transform', `rotate(${heading}, ${Math.sin(angle) * textR}, ${-Math.cos(angle) * textR + 4})`);
        text.textContent = cardinals[deg] || (deg / 10).toString();
        rose.appendChild(text);
      }
    }
  }
}

// Initialize when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
  window.cockpit = new CockpitDisplay();
});
