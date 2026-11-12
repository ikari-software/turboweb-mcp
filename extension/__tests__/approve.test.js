import { describe, it, expect } from 'vitest';
import fs from 'fs';
import path from 'path';
import { createRequire } from 'module';

const require = createRequire(import.meta.url);
const { describeOrigin, renderApproval } = require('../approve.js');

function page() {
  const html = fs.readFileSync(path.resolve(__dirname, '../approve.html'), 'utf8');
  document.body.innerHTML = html.match(/<body[^>]*>([\s\S]*)<script/)[1];
  return document;
}

describe('describeOrigin', () => {
  it('recognises this computer', () => {
    for (const remote of ['127.0.0.1:54321', '[::1]:54321', 'in-process']) {
      expect(describeOrigin(remote, '')).toMatchObject({ local: true, label: 'This computer', warning: '' });
    }
  });

  it('warns about a connection from another device', () => {
    const from = describeOrigin('192.168.1.20:40000', '');
    expect(from.local).toBe(false);
    expect(from.label).toBe('Another device: 192.168.1.20');
    expect(from.warning).toMatch(/another device \(192\.168\.1\.20\).*do not allow/);
  });

  it('warns when the origin is unknown or a web page opened the connection', () => {
    expect(describeOrigin('', '').warning).toMatch(/could not tell where/);
    expect(describeOrigin('127.0.0.1:5000', 'http://localhost:3000').warning).toMatch(/opened by a web page \(http:\/\/localhost:3000\)/);
    expect(describeOrigin('127.0.0.1:5000', 'chrome-extension://abc').warning).toBe('');
  });
});

describe('renderApproval', () => {
  it('shows who is asking, why, and from where', () => {
    const doc = page();
    renderApproval(doc, { name: 'Claude', model: 'some-model', hostName: 'Claude Code', project: 'ffsync', pid: 42,
      task: 'Sorting tabs by topic', remote: '127.0.0.1:54321', origin: '' });
    expect(doc.getElementById('name').textContent).toBe('Claude');
    expect(doc.getElementById('about').textContent).toBe('some-model · Claude Code · in ffsync · pid 42');
    expect(doc.getElementById('task').textContent).toBe('Sorting tabs by topic');
    expect(doc.getElementById('from').textContent).toBe('This computer');
    expect(doc.getElementById('warn').classList.contains('on')).toBe(false);
  });

  it('writes what the agent says as text, never as markup', () => {
    const doc = page();
    renderApproval(doc, { name: '<img src=x onerror=alert(1)>', task: '<b>trust me</b>', remote: '10.0.0.5:1' });
    expect(doc.querySelector('dl img, dl b')).toBeNull();
    expect(doc.getElementById('name').textContent).toBe('<img src=x onerror=alert(1)>');
    expect(doc.getElementById('warn').classList.contains('on')).toBe(true);
  });

  it('says so when the agent gave no purpose or comes through an older TurboWeb', () => {
    const doc = page();
    renderApproval(doc, { name: 'Claude', remote: '127.0.0.1:1' });
    expect(doc.getElementById('task').textContent).toBe('It did not say.');
    renderApproval(doc, { id: 'legacy', name: 'Unknown agent', legacy: true });
    expect(doc.getElementById('task').textContent).toMatch(/older TurboWeb/);
    expect(doc.getElementById('from').textContent).toBe('Unknown (older TurboWeb)');
  });
});
