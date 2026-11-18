(() => {
  'use strict';

  // Which character's body gestures are emphasized during each cue.
  const leads = {
    intro: ['both', 'snake', 'gopher'],
    build: ['snake', 'gopher', 'snake', 'gopher'],
    bridge: ['snake', 'gopher', 'gopher', 'snake'],
    packages: ['snake', 'gopher', 'snake', 'snake'],
    threads: ['gopher', 'gopher', 'snake', 'snake'],
    request: ['both', 'gopher', 'snake', 'snake', 'gopher', 'snake'],
    deploy: ['gopher', 'snake', 'snake', 'gopher'],
    outro: ['both', 'both'],
  };

  const byId = id => document.getElementById(id);
  const gopher = byId('presenter-gopher');
  const leftArm = byId('gopher-left-arm-line');
  const rightArm = byId('gopher-right-arm-line');
  const supportingArms = byId('supporting-arms');
  const holdingPaws = byId('holding-paws');
  const leftLeg = byId('gopher-left-leg');
  const rightLeg = byId('gopher-right-leg');
  const torso = byId('gopher-torso');
  const snake = byId('presenter-snake');
  const ears = byId('snake-ears');
  const face = byId('snake-face');
  const skin = byId('snake-skin');
  const gopherPupils = [byId('gopher-left-pupil'), byId('gopher-right-pupil')];
  const snakePupils = [byId('snake-left-pupil'), byId('snake-right-pupil')];
  const gopherMouth = byId('gopher-mouth');
  const gopherSnout = byId('gopher-snout');
  const gopherNose = byId('gopher-nose');
  const gopherJaw = byId('gopher-upper-jaw');
  const { rate: speechRate, levels } = window.PYTHAGO_SPEECH;
  const speechLevels = Uint8Array.from(atob(levels), byte => byte.charCodeAt(0));

  const headX = 51;
  const headY = 110;
  const headAngle = Math.atan2(23, 3);
  // Twelve fixed-length bones and thirteen radii, in order from head to tail.
  const lengths = [21, 20, 20, 19, 19, 19, 19, 18, 18, 17, 16, 15];
  const radii = [19, 17, 15, 13, 12, 11, 10, 9, 8, 7, 5, 3, 0];
  // Shoulder, two curve controls, and paw in gopher-local coordinates.
  const openLeft = [56, 116, 19, 153, 19, 193, 32, 205];
  const cradleLeft = [56, 116, 8, 158, 63, 198, 111, 170];
  const openRight = [189, 112, 222, 154, 183, 211, 162, 189];
  const cradleRight = [189, 112, 223, 164, 196, 215, 167, 170];
  const heldAngles = [1.18, 0.78, 0.35, 0.08, -0.06, 0.06, 0.16, 0.24, 0.08, -0.26, -0.54, -0.88];
  const tuckAngles = [0, 0, 0, 0, 0, 0, -0.12, -0.5, -1.0, -1.55, -2.13, -2.65];

  function ease(value) {
    const x = Math.max(0, Math.min(1, value));
    return x * x * (3 - 2 * x);
  }

  function gesture(time, enter, hold, release, end) {
    return ease((time - enter) / (hold - enter)) * ease((end - time) / (end - release));
  }

  function blend(a, b, weight) {
    return a + (b - a) * weight;
  }

  function bendArm(path, x, y, direction, shoulder, elbow, cradle, hold) {
    const jointX = x + direction * 36 * Math.cos(shoulder);
    const jointY = y + 36 * Math.sin(shoulder);
    const forearm = shoulder + elbow;
    const pawX = jointX + direction * 39 * Math.cos(forearm);
    const pawY = jointY + 39 * Math.sin(forearm);
    // Express the usual two-joint pose as a cubic so it can ease into a cradle.
    const points = [
      x, y, blend(x, jointX, 2 / 3), blend(y, jointY, 2 / 3),
      blend(pawX, jointX, 2 / 3), blend(pawY, jointY, 2 / 3), pawX, pawY,
    ].map((value, i) => blend(value, cradle[i], hold));
    path.setAttribute('d', `M ${points.slice(0, 2).join(' ')} C ${points.slice(2).join(' ')}`);
  }

  function poseGopher(time, beat, lead, wave, cuddle, hold, wrap, salute) {
    const armAccent = lead * (0.7 + 0.3 * Math.cos(beat * (wave ? 4.6 : 2.4)));
    const leftStep = (1 - cuddle) * lead * Math.pow(Math.sin(beat * 2.0), 4);
    const rightStep = (1 - cuddle) * lead * Math.pow(Math.sin(beat * 2.0 + Math.PI / 2), 4);
    const x = 7 * Math.sin(time * 0.55) + 5 * cuddle;
    const y = 2 * Math.sin(time * 2);
    gopher.setAttribute('transform', `translate(${x} ${y})`);
    const lean = blend(2 * Math.sin(time * 1.4) - 4 * armAccent, -4, cuddle);
    torso.setAttribute('transform', `rotate(${lean} 122 185)`);
    const nearArm = openLeft.map((coord, i) => blend(coord, cradleLeft[i], wrap));
    bendArm(leftArm, 60, 115, -1, -0.26 - 0.52 * armAccent + 0.12 * Math.sin(time * 2),
      -0.50 + 0.24 * Math.sin(time * 3.2), nearArm, hold);
    // Raise the hand to greet and sign off, then reach toward the diagram.
    const rightShoulder = -0.26 + (wave ? -0.47 : 0.35) * armAccent - 0.63 * salute;
    const rightElbow = -0.48 + (wave ? -0.32 : 0.37) * armAccent + 0.27 * Math.sin(time * 5) * salute;
    const farArm = openRight.map((coord, i) => blend(coord, cradleRight[i], wrap));
    bendArm(rightArm, 182, 114, 1, rightShoulder, rightElbow, farArm, hold);
    // Both arms support from behind. The paws close over the belly after
    // the lift and stay clipped below the head.
    const armPosition = `translate(${125 + x} ${500 + y})`;
    supportingArms.setAttribute('transform', armPosition);
    supportingArms.setAttribute('opacity', ease((hold - 0.3) / 0.35));
    holdingPaws.setAttribute('transform', armPosition);
    holdingPaws.setAttribute('opacity', ease(wrap / 0.25));
    leftLeg.setAttribute('transform', `translate(0 ${-9 * leftStep}) rotate(${-11 * leftStep} 85 185)`);
    rightLeg.setAttribute('transform', `translate(0 ${-9 * rightStep}) rotate(${11 * rightStep} 157 185)`);
    return { x, y };
  }

  // Interpolate the outline with cubics so there are no corners at the joints.
  function curve(points) {
    let path = '';
    for (let i = 0; i < points.length - 1; i++) {
      const [ax, ay] = points[Math.max(0, i - 1)];
      const [bx, by] = points[i];
      const [cx, cy] = points[i + 1];
      const [dx, dy] = points[Math.min(points.length - 1, i + 2)];
      path += ` C ${bx + (cx - ax) / 6} ${by + (cy - ay) / 6} ${cx - (dx - bx) / 6} ${cy - (dy - by) / 6} ${cx} ${cy}`;
    }
    return path;
  }

  function poseSnake(time, beat, lead, cuddle, lift, salute, gopherPosition) {
    const headTilt = 0.08 * Math.sin(time * 1.8) + lead * (0.12 + 0.13 * Math.sin(beat * 2.3));
    const stride = time * 2.5;
    // Periodically gather the tail into a loose curl during a listening beat.
    const tuck = (1 - lead) * ease((Math.cos(time * 0.42 + 2) - 0.18) / 0.7);
    const joints = [[headX, 0]];
    const angles = [];
    // Send a lateral wave down the spine. Keep the neck raised above the body.
    const neckAngles = [headAngle - 0.26 + lead * 0.20 + headTilt, 0.69 + lead * 0.30, 0.26 + lead * 0.16];
    for (const [i, length] of lengths.entries()) {
      const wave = (0.62 - 0.22 * tuck) * (1 - 0.55 * cuddle) * ease((i - 1) / 3) * Math.sin(stride - i * 1.06);
      const flick = -salute * ease((i - 6) / 5) * (0.66 + 0.55 * Math.sin(time * 7 - 0.6 * i));
      const freeAngle = (neckAngles[i] ?? 0) + wave + tuck * tuckAngles[i] + flick;
      const angle = blend(freeAngle, heldAngles[i] + 0.045 * Math.sin(stride - i), cuddle);
      const [x, y] = joints[i];
      joints.push([x + length * Math.cos(angle), y + length * Math.sin(angle)]);
      angles.push(angle);
    }
    // Keep the middle of the body at a consistent level as the head rises.
    const rootY = 156 - (joints[4][1] + joints[5][1] + joints[6][1]) / 3;

    const left = [];
    const right = [];
    for (const [i, [x, y]] of joints.entries()) {
      const before = angles[Math.max(0, i - 1)];
      const after = angles[Math.min(i, angles.length - 1)];
      const normal = Math.atan2(Math.sin(before) + Math.sin(after), Math.cos(before) + Math.cos(after)) + Math.PI / 2;
      const radius = radii[i];
      const dx = radius * Math.cos(normal);
      const dy = radius * Math.sin(normal);
      left.push([x + dx, y + dy + rootY]);
      right.push([x - dx, y - dy + rootY]);
    }

    const [lx, ly] = left[0];
    const [rx, ry] = right[0];
    const capX = -28 * Math.cos(angles[0]);
    const capY = -28 * Math.sin(angles[0]);
    const headCap = `C ${rx + capX} ${ry + capY} ${lx + capX} ${ly + capY} ${lx} ${ly}`;
    // The reversed side starts at the shared tail point and continues to the head.
    skin.setAttribute('d', `M ${lx} ${ly}${curve(left)}${curve(right.reverse())} ${headCap} Z`);
    const head = `translate(0 ${rootY - headY}) rotate(${(angles[0] - headAngle) * 180 / Math.PI} ${headX} ${headY})`;
    ears.setAttribute('transform', head);
    face.setAttribute('transform', head);
    const travel = 22 * Math.sin(time * 0.46) + 4 * Math.sin(stride);
    const cuddleX = 188 + gopherPosition.x - (370 + 1.36 * headX);
    const cuddleY = 618 + gopherPosition.y - (516 + 1.36 * rootY);
    snake.setAttribute('transform', `translate(${blend(travel, cuddleX, cuddle)} ${cuddleY * lift})`);
  }

  function poseEyes(time, gopherLead, snakeLead, cuddle, wrap) {
    const gopherX = blend(3 + 4 * gopherLead + 2 * Math.sin(time * 0.86), 0, cuddle);
    const gopherY = blend(-4 * gopherLead + 5 * snakeLead + 1.5 * Math.sin(time * 1.1), 8 + 6 * wrap, cuddle);
    const snakeX = blend(2.3 * (snakeLead - gopherLead) + 0.65 * Math.sin(time * 1.2 + 1), 2.5, cuddle);
    const snakeY = -0.9 * snakeLead - 0.6 * gopherLead + 0.55 * Math.sin(time * 1.45);
    for (const pupil of gopherPupils) pupil.setAttribute('transform', `translate(${gopherX} ${gopherY})`);
    for (const pupil of snakePupils) pupil.setAttribute('transform', `translate(${snakeX} ${snakeY})`);
  }

  function speechAt(time) {
    const position = time * speechRate;
    const i = Math.floor(position);
    return blend(speechLevels[i] ?? 0, speechLevels[i + 1] ?? 0, position - i) / 255;
  }

  function poseGopherSpeech(speech) {
    gopherMouth.setAttribute('rx', 11 + 7 * speech);
    gopherMouth.setAttribute('ry', 11 * speech);
    gopherSnout.setAttribute('rx', 22 + 1.9 * speech);
    gopherSnout.setAttribute('ry', 15.5 - 0.65 * speech);
    gopherJaw.setAttribute('transform', `translate(0 ${-2.8 * speech})`);
    gopherNose.setAttribute('transform', `translate(${1.1 * speech} ${-4.2 * speech})`);
  }

  window.posePythagoPresenters = (time, scene, cue, duration) => {
    const [start, end] = cue;
    const beat = time - start;
    const energy = ease(beat / 0.55) * ease((end - time) / 0.7);
    const lead = leads[scene.diagram][scene.cues.indexOf(cue)];
    const wave = scene.diagram === 'intro' || scene.diagram === 'outro';
    const gopherLead = lead === 'snake' ? 0 : energy;
    const snakeLead = lead === 'gopher' ? 0 : energy;
    const cuddle = Math.max(gesture(time, 7.8, 9.3, 11.2, 13.4), gesture(time, duration - 9, duration - 7, duration - 4, duration - .2));
    const hold = Math.max(gesture(time, 7.1, 8.8, 12.6, 14), gesture(time, duration - 9.8, duration - 7.6, duration - 1.5, duration));
    const lift = Math.max(gesture(time, 8.9, 9.7, 11.2, 12.1), gesture(time, duration - 7.6, duration - 6.5, duration - 4, duration - 2.7));
    // Close the near arm after the lift, then open it before setting the snake down.
    const wrap = Math.max(gesture(time, 9.7, 10.16, 10.84, 11.2), gesture(time, duration - 6.5, duration - 6, duration - 4.5, duration - 4));
    const salute = gesture(time, 94.5, 95.2, 96.7, 97.5);
    const gopherPosition = poseGopher(time, beat, gopherLead, wave, cuddle, hold, wrap, salute);
    poseSnake(time, beat, snakeLead, cuddle, lift, salute, gopherPosition);
    poseEyes(time, gopherLead, snakeLead, cuddle, wrap);
    const speech = speechAt(time) * ease(beat / 0.09) * ease((end - time) / 0.09);
    poseGopherSpeech(speech);
    return cuddle;
  };
})();
