// The live view's page. It draws the pictures the server sends, shows what the
// agent did, and, while the person has control, forwards their input.
//
// Everything that comes from the server is put into the page as text. Page
// titles and addresses come from whatever site the agent is on, so none of it
// may ever be read as markup.
(() => {
  'use strict';
  const $ = (id) => document.getElementById(id);
  const canvas = $('view');
  const ctx = canvas.getContext('2d');
  const takeBtn = $('take');
  const giveBtn = $('give');
  const sayText = $('say-text');
  const sayBtn = $('say-send');

  let ws = null;
  let you = false;
  let who = 'agent';
  let open = false;
  let frames = 0;
  let delay = 500;

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
  }

  function banner(text) { $('banner').textContent = text; }

  // Giving back disables the button that was just pressed, and a disabled
  // button cannot hold focus, so focus is moved on once the answer arrives.
  let focusTakeNext = false;

  function setControl(whoNow, youNow) {
    who = whoNow;
    you = youNow;
    takeBtn.disabled = !(ws && ws.readyState === WebSocket.OPEN) || who === 'person';
    giveBtn.disabled = !you;
    sayText.disabled = !you;
    sayBtn.disabled = !you;
    if (you) {
      banner('You have control. The agent waits until you give it back.');
    } else if (who === 'person') {
      banner('Someone else has control.');
    } else {
      banner('The agent has control. You are watching.');
    }
    document.body.dataset.control = you ? 'you' : who;
    if (focusTakeNext && !you && !takeBtn.disabled) {
      focusTakeNext = false;
      takeBtn.focus();
    }
  }

  function setMeta(m) {
    open = m.open;
    $('title').textContent = m.title || (m.open ? 'Untitled page' : 'No tab is open yet');
    $('url').textContent = m.url || '';
  }

  function showFrame(blob) {
    createImageBitmap(blob).then((bmp) => {
      if (canvas.width !== bmp.width || canvas.height !== bmp.height) {
        canvas.width = bmp.width;
        canvas.height = bmp.height;
      }
      ctx.drawImage(bmp, 0, 0);
      bmp.close();
      frames += 1;
      document.body.dataset.frames = String(frames);
    });
  }

  function addEvent(e) {
    const li = document.createElement('li');
    const time = new Date(e.time);
    const when = time.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    const head = document.createElement('span');
    head.className = 'mute';
    head.textContent = when + ' ';
    li.appendChild(head);
    const tool = document.createElement('strong');
    tool.textContent = e.tool;
    li.appendChild(tool);
    if (e.error) {
      const err = document.createElement('span');
      err.className = 'err';
      err.textContent = ' Error: ' + e.error;
      li.appendChild(err);
    } else if (e.summary) {
      li.appendChild(document.createTextNode(' ' + e.summary));
    }
    const took = document.createElement('span');
    took.className = 'mute';
    took.textContent = ' (' + (e.elapsedMs || 0) + ' ms)';
    li.appendChild(took);
    const log = $('log');
    log.appendChild(li);
    while (log.children.length > 200) log.removeChild(log.firstChild);
    $('logbox').scrollTop = $('logbox').scrollHeight;
    $('log-empty').hidden = true;
  }

  function setApprovals(list) {
    const ul = $('approvals');
    ul.textContent = '';
    $('approvals-empty').hidden = list.length > 0;
    for (const p of list) {
      const li = document.createElement('li');
      const what = [p.tool, p.element, p.detail].filter(Boolean).join(' ');
      const text = document.createElement('p');
      text.textContent = what + (p.reason ? ' (' + p.reason + ')' : '');
      li.appendChild(text);
      const row = document.createElement('div');
      row.className = 'row';
      for (const approve of [true, false]) {
        const b = document.createElement('button');
        b.type = 'button';
        b.textContent = approve ? 'Approve' : 'Deny';
        if (!approve) b.className = 'deny';
        b.setAttribute('aria-label', (approve ? 'Approve ' : 'Deny ') + what);
        b.addEventListener('click', () => send({ t: 'resolve', id: p.id, approve }));
        row.appendChild(b);
      }
      li.appendChild(row);
      ul.appendChild(li);
    }
  }

  function handle(m) {
    switch (m.t) {
      case 'hello': $('devtools').textContent = m.devtools || 'Not known.'; break;
      case 'meta': setMeta(m); break;
      case 'control': setControl(m.who, m.you); break;
      case 'event': addEvent(m); break;
      case 'events': $('log').textContent = ''; $('log-empty').hidden = m.list.length > 0; for (const e of m.list) addEvent(e); break;
      case 'approvals': setApprovals(m.list); break;
      case 'error': banner(m.message); break;
    }
  }

  function connect() {
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(scheme + '://' + location.host + '/ws');
    ws.binaryType = 'blob';
    ws.onopen = () => {
      delay = 500;
      setControl('agent', false);
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') handle(JSON.parse(ev.data));
      else showFrame(ev.data);
    };
    ws.onclose = () => {
      you = false;
      takeBtn.disabled = true;
      giveBtn.disabled = true;
      sayText.disabled = true;
      sayBtn.disabled = true;
      banner('Disconnected from Raulí. Trying again.');
      document.body.dataset.control = 'none';
      setTimeout(connect, delay);
      delay = Math.min(delay * 2, 5000);
    };
  }

  takeBtn.addEventListener('click', () => { send({ t: 'take' }); });
  giveBtn.addEventListener('click', () => { focusTakeNext = true; send({ t: 'release' }); });

  $('say').addEventListener('submit', (ev) => {
    ev.preventDefault();
    if (!you || sayText.value === '') return;
    send({ t: 'text', text: sayText.value });
    sayText.value = '';
  });

  // A position is sent as a fraction of the picture, so the server can place it
  // on the page whatever size the picture is shown at.
  function frac(ev) {
    const r = canvas.getBoundingClientRect();
    return { x: (ev.clientX - r.left) / r.width, y: (ev.clientY - r.top) / r.height };
  }
  function mods(ev) { return (ev.altKey ? 1 : 0) | (ev.ctrlKey ? 2 : 0) | (ev.metaKey ? 4 : 0) | (ev.shiftKey ? 8 : 0); }
  const buttons = ['left', 'middle', 'right'];

  let lastMove = 0;
  canvas.addEventListener('mousemove', (ev) => {
    if (!you) return;
    const now = performance.now();
    if (now - lastMove < 40) return;
    lastMove = now;
    send(Object.assign({ t: 'pointer', type: 'move', button: 'none', mods: mods(ev) }, frac(ev)));
  });
  canvas.addEventListener('mousedown', (ev) => {
    canvas.focus();
    if (!you) return;
    ev.preventDefault();
    send(Object.assign({ t: 'pointer', type: 'down', button: buttons[ev.button] || 'left', clicks: ev.detail || 1, mods: mods(ev) }, frac(ev)));
  });
  canvas.addEventListener('mouseup', (ev) => {
    if (!you) return;
    ev.preventDefault();
    send(Object.assign({ t: 'pointer', type: 'up', button: buttons[ev.button] || 'left', clicks: ev.detail || 1, mods: mods(ev) }, frac(ev)));
  });
  canvas.addEventListener('contextmenu', (ev) => { if (you) ev.preventDefault(); });
  canvas.addEventListener('wheel', (ev) => {
    if (!you) return;
    ev.preventDefault();
    const unit = ev.deltaMode === 1 ? 16 : 1;
    send(Object.assign({ t: 'wheel', dx: ev.deltaX * unit, dy: ev.deltaY * unit }, frac(ev)));
  }, { passive: false });

  // A key that makes a character is sent with that character; any other key is
  // sent as a key alone. Shift+Escape is kept for the person, as the way out of
  // the picture, because a picture that forwards every key would trap the keyboard.
  canvas.addEventListener('keydown', (ev) => {
    if (!you) return;
    if (ev.key === 'Escape' && ev.shiftKey) {
      ev.preventDefault();
      takeBtn.disabled ? giveBtn.focus() : takeBtn.focus();
      return;
    }
    ev.preventDefault();
    const printable = [...ev.key].length === 1 && !ev.ctrlKey && !ev.metaKey && !ev.altKey;
    send({ t: 'key', type: 'down', key: ev.key, code: ev.code, vk: ev.keyCode, mods: mods(ev), text: printable ? ev.key : '' });
  });
  canvas.addEventListener('keyup', (ev) => {
    if (!you) return;
    if (ev.key === 'Escape' && ev.shiftKey) return;
    ev.preventDefault();
    send({ t: 'key', type: 'up', key: ev.key, code: ev.code, vk: ev.keyCode, mods: mods(ev) });
  });
  canvas.addEventListener('paste', (ev) => {
    if (!you) return;
    ev.preventDefault();
    const text = ev.clipboardData ? ev.clipboardData.getData('text') : '';
    if (text) send({ t: 'text', text });
  });

  setControl('agent', false);
  connect();
})();
