// The approval prompt. Everything shown here comes from the agent or the
// daemon, so it is only ever written as text, never as markup.

// describeOrigin says where an agent's connection comes from in words a
// person can judge: this computer, or somewhere else (which is never normal).
function describeOrigin(remote, origin) {
  const host = String(remote || '').replace(/:\d+$/, '').replace(/^\[|\]$/g, '');
  const local = host === 'in-process' || host === 'localhost' || host === '::1' || /^127\./.test(host);
  const fromPage = /^https?:/i.test(origin || '');
  let warning = '';
  if (!local) {
    warning = host
      ? `This connection comes from another device (${host}), not from this computer. Unless you set that up yourself, do not allow it.`
      : 'TurboWeb could not tell where this connection comes from. Unless you know why, do not allow it.';
  } else if (fromPage) {
    warning = `This connection was opened by a web page (${origin}), not by a program you started. Do not allow it unless you expect that.`;
  }
  return {
    local,
    label: local ? 'This computer' : (host ? `Another device: ${host}` : 'Unknown'),
    detail: [remote && remote !== 'in-process' ? remote : '', origin ? `origin ${origin}` : ''].filter(Boolean).join(' · '),
    warning,
  };
}

function renderApproval(doc, client) {
  const set = (id, text) => { doc.getElementById(id).textContent = text; };
  set('name', client.name || 'Unnamed agent');
  set('about', [client.model, client.hostName, client.project && `in ${client.project}`, client.pid ? `pid ${client.pid}` : '']
    .filter(Boolean).join(' · '));
  set('task', client.task || (client.legacy
    ? 'Nothing: it is connected through an older TurboWeb that does not say who is asking.'
    : 'It did not say.'));
  const from = describeOrigin(client.remote, client.origin);
  set('from', client.legacy ? 'Unknown (older TurboWeb)' : from.label);
  set('from-detail', from.detail);
  const warn = doc.getElementById('warn');
  warn.textContent = client.legacy ? '' : from.warning;
  warn.classList.toggle('on', !!warn.textContent);
}

if (typeof document !== 'undefined' && typeof chrome !== 'undefined' && document.getElementById('allow')) {
  const id = new URLSearchParams(location.search).get('id');
  const decide = (allow) => chrome.runtime.sendMessage({ type: 'approval_decide', id, allow }, () => window.close());
  chrome.runtime.sendMessage({ type: 'approval_get', id }, (reply) => {
    if (!reply || reply.gone) { window.close(); return; }
    renderApproval(document, reply.client);
    document.getElementById('allow').addEventListener('click', () => decide(true));
    document.getElementById('deny').addEventListener('click', () => decide(false));
  });
}

if (typeof module !== 'undefined') module.exports = { describeOrigin, renderApproval };
