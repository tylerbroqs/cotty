// cotty web client: speaks the same JSON-over-websocket protocol as
// `cotty join`, including end-to-end encryption via WebCrypto. The session
// key arrives in the URL fragment (#code=...&k=...), which browsers never
// send over the network.
(function () {
  'use strict';

  const statusEl = document.getElementById('status');
  const activityEl = document.getElementById('activity');
  const joinEl = document.getElementById('join');
  const termEl = document.getElementById('term');
  const toastEl = document.getElementById('toast');

  let activityTimer = null;
  function showActivity(name) {
    activityEl.textContent = name + ' is typing…';
    clearTimeout(activityTimer);
    activityTimer = setTimeout(() => { activityEl.textContent = ''; }, 2500);
  }

  function setStatus(text) { statusEl.textContent = text; }

  let toastTimer = null;
  function toast(text) {
    toastEl.textContent = text;
    toastEl.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toastEl.classList.remove('show'), 4000);
  }

  function b64Decode(s) {
    const bin = atob(s);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  function b64Encode(bytes) {
    let bin = '';
    for (const b of bytes) bin += String.fromCharCode(b);
    return btoa(bin);
  }

  function b64urlDecode(s) {
    return b64Decode(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - s.length % 4) % 4));
  }

  function params() {
    return new URLSearchParams(location.hash.slice(1));
  }

  function showForm() {
    joinEl.hidden = false;
    setStatus('not connected');
    document.getElementById('f-go').addEventListener('click', () => {
      const code = document.getElementById('f-code').value.trim();
      const name = document.getElementById('f-name').value.trim();
      const key = document.getElementById('f-key').value.trim().replace(/^#?k=/, '');
      if (!code) return;
      let hash = 'code=' + encodeURIComponent(code);
      if (name) hash += '&name=' + encodeURIComponent(name);
      if (key) hash += '&k=' + encodeURIComponent(key);
      location.hash = hash;
      location.reload();
    });
  }

  async function connect(code, name, keyStr) {
    termEl.hidden = false;

    const term = new Terminal({
      fontSize: 14,
      scrollback: 5000,
      theme: { background: '#101418', foreground: '#f0f6fc', cursor: '#ff9500' },
    });
    const fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(termEl);
    fit.fit();

    // Track the host's size once announced; until then, fit the window.
    let hostSized = false;
    window.addEventListener('resize', () => { if (!hostSized) fit.fit(); });

    // Our session name, from the host's welcome; votes carry it.
    let me = name || '';
    let cryptoKey = null;
    if (keyStr) {
      try {
        cryptoKey = await crypto.subtle.importKey(
          'raw', b64urlDecode(keyStr), 'AES-GCM', false, ['encrypt', 'decrypt']);
      } catch {
        setStatus('invalid session key in URL');
        return;
      }
    }

    async function open(box) {
      const nonce = box.slice(0, 12);
      const ct = box.slice(12);
      return new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: nonce }, cryptoKey, ct));
    }

    async function seal(plain) {
      const nonce = crypto.getRandomValues(new Uint8Array(12));
      const ct = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv: nonce }, cryptoKey, plain));
      const out = new Uint8Array(nonce.length + ct.length);
      out.set(nonce);
      out.set(ct, nonce.length);
      return out;
    }

    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const q = new URLSearchParams({ code: code });
    if (name) q.set('name', name);
    const ws = new WebSocket(proto + '://' + location.host + '/ws?' + q.toString());

    let failed = false;
    function fail(text) {
      failed = true;
      setStatus(text);
      toast(text);
      ws.close();
    }

    // Frames must be applied in order even though decryption is async, so
    // handlers run on a promise chain.
    let chain = Promise.resolve();
    ws.onmessage = (ev) => {
      chain = chain.then(() => handle(JSON.parse(ev.data))).catch(() => {});
    };

    async function handle(msg) {
      switch (msg.type) {
        case 'hello': {
          if (msg.enc && !cryptoKey) {
            fail('this session is end-to-end encrypted — open the full link, including its #k= part');
            return;
          }
          if (!msg.enc && cryptoKey) {
            fail('the link carries a session key but this session is not encrypted — refusing to join');
            return;
          }
          let mode = msg.writable ? 'read-write' : 'view-only';
          if (msg.enc) mode += ', end-to-end encrypted';
          setStatus(msg.text + ' (' + mode + ')');
          const i = (msg.text || '').lastIndexOf('you are ');
          if (i >= 0) me = msg.text.slice(i + 8).trim();
          break;
        }
        case 'output': {
          let data = b64Decode(msg.data || '');
          if (cryptoKey) {
            try {
              data = await open(data);
            } catch {
              fail('cannot decrypt session output — wrong session key?');
              return;
            }
          }
          term.write(data);
          break;
        }
        case 'resize':
          if (msg.cols > 0 && msg.rows > 0) {
            hostSized = true;
            term.resize(msg.cols, msg.rows);
          }
          break;
        case 'info':
          toast(msg.text);
          break;
        case 'writable':
          toast('your connection is now ' + (msg.writable ? 'read-write' : 'view-only'));
          break;
        case 'activity':
          if (msg.name) showActivity(msg.name);
          break;
        case 'proposal': {
          // An agent's command held for multisig approval.
          let data = b64Decode(msg.data || '');
          if (cryptoKey) {
            try { data = await open(data); } catch { return; }
          }
          showProposal(JSON.parse(new TextDecoder().decode(data)));
          break;
        }
        case 'say': {
          // An agent's remarks; its answers to `cotty ask` show up in the
          // terminal itself.
          let data = b64Decode(msg.data || '');
          if (cryptoKey) {
            try {
              data = await open(data);
            } catch {
              return;
            }
          }
          toast((msg.name || 'agent') + ': ' + new TextDecoder().decode(data));
          break;
        }
      }
    }

    // Keystrokes and votes: sealed (in order) when the session is
    // encrypted.
    let sendChain = Promise.resolve();
    function sendSealed(type, bytes) {
      sendChain = sendChain.then(async () => {
        let data = bytes;
        if (cryptoKey) data = await seal(data);
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: type, data: b64Encode(data) }));
        }
      }).catch(() => {});
    }

    // Multisig: one card per pending proposal, with vote buttons for
    // approvers; cards go away once a proposal is settled.
    const votesEl = document.getElementById('votes');
    const cards = new Map();
    function showProposal(p) {
      let card = cards.get(p.id);
      if (p.state !== 'pending') {
        if (card) { card.remove(); cards.delete(p.id); }
        return;
      }
      if (!card) {
        card = document.createElement('div');
        card.className = 'proposal';
        votesEl.appendChild(card);
        cards.set(p.id, card);
      }
      card.textContent = '';
      const head = document.createElement('div');
      head.textContent = '#' + p.id + ' ' + p.who + ' wants to run:';
      const cmd = document.createElement('code');
      cmd.className = 'cmd';
      cmd.textContent = p.command;
      const tally = document.createElement('div');
      tally.className = 'tally';
      tally.textContent = (p.approved || []).length + ' of ' + p.quorum + ' approvals from ' + p.approvers.join(', ') +
        ((p.approved || []).length ? ' · approved by ' + p.approved.join(', ') : '');
      card.append(head, cmd, tally);
      const voted = (p.approved || []).includes(me) || (p.rejected || []).includes(me);
      if (p.approvers.includes(me) && !voted) {
        const row = document.createElement('div');
        row.style.marginTop = '8px';
        for (const [op, label, cls] of [['approve', 'Approve', 'yes'], ['reject', 'Reject', 'no']]) {
          const b = document.createElement('button');
          b.className = cls;
          b.textContent = label;
          b.onclick = () => {
            row.remove();
            sendSealed('vote', new TextEncoder().encode(JSON.stringify({ id: p.id, op: op, voter: me })));
          };
          row.appendChild(b);
        }
        card.appendChild(row);
      }
    }
    term.onData((s) => {
      sendChain = sendChain.then(async () => {
        let data = new TextEncoder().encode(s);
        if (cryptoKey) data = await seal(data);
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'input', data: b64Encode(data) }));
        }
      }).catch(() => {});
    });

    ws.onclose = () => { if (!failed) setStatus('disconnected'); };
    ws.onerror = () => { if (!failed) setStatus('connection failed'); };
    term.focus();
  }

  const p = params();
  const code = (p.get('code') || '').trim();
  if (!code) {
    showForm();
  } else {
    connect(code, (p.get('name') || '').trim(), (p.get('k') || '').trim());
  }
})();
