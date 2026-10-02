'use strict';

const $ = id => document.getElementById(id);
const state = { register: false, next: null, maxBytes: 52428800, selected: null, uploading: false, noticeTimer: null };

/* Bootstrap */
async function start() {
  $('auth-form').addEventListener('submit', submitAuth);
  $('auth-switch').addEventListener('click', () => setAuthMode(!state.register));
  $('logout').addEventListener('click', logout);
  $('upload').addEventListener('click', () => $('file').click());
  $('file').addEventListener('change', () => upload($('file').files[0]));
  $('refresh').addEventListener('click', () => perform($('refresh'), loadFiles));
  $('more').addEventListener('click', () => perform($('more'), () => loadFiles(true)));
  $('rename-form').addEventListener('submit', renameFile);
  $('delete-form').addEventListener('submit', deleteFile);
  $('notice-close').addEventListener('click', () => notice());
  document.querySelectorAll('[data-close]').forEach(button => button.onclick = () => $(button.dataset.close).close());
  connectDropZone();

  try { await showFiles(await request('/me')); }
  catch (error) { showSignIn(); if (error.status !== 401) notice(error.message); }
}

/* Authentication */
function submitAuth(event) {
  event.preventDefault();
  perform($('auth'), async () => {
    const password = $('password').value;
    if ([...password].length < 8) throw new Error('Password must be at least 8 characters.');
    const path = state.register ? '/auth/register' : '/auth/login';
    await showFiles(await request(path, json('POST', { username: $('username').value, password })));
  }, $('auth-error'));
}

function setAuthMode(register) {
  state.register = register;
  const title = register ? 'Create account' : 'Sign in';
  $('auth-title').textContent = $('auth-submit-label').textContent = title;
  $('auth-switch').textContent = register ? 'Sign in' : 'Create account';
  $('password').autocomplete = register ? 'new-password' : 'current-password';
  $('password').value = '';
  $('auth-error').hidden = true;
  notice();
  $('username').focus();
}

function logout() {
  perform($('logout'), async () => {
    await request('/auth/logout', { method: 'POST' });
    $('files').replaceChildren();
    showSignIn();
  });
}

function showSignIn() {
  setAuthMode(false);
  $('workspace').hidden = $('account').hidden = true;
  $('auth').hidden = false;
  $('password').value = $('file').value = '';
  document.querySelectorAll('dialog[open]').forEach(dialog => dialog.close());
}

async function showFiles(user) {
  $('current-user').textContent = user.username;
  $('password').value = '';
  $('auth').hidden = true;
  $('account').hidden = $('workspace').hidden = false;
  await loadFiles();
}

/* Files */
async function loadFiles(append = false) {
  $('list-status').textContent = 'Loading…';
  $('empty').hidden = $('more').hidden = true;
  try {
    const suffix = append && state.next ? `&before=${state.next}` : '';
    const data = await request(`/files?limit=20${suffix}`);
    if (!append) $('files').replaceChildren();
    data.items.forEach(renderFile);
    state.next = data.next_before;
    state.maxBytes = data.max_file_bytes;
    $('list-status').textContent = `${formatSize(state.maxBytes)} upload limit`;
    $('empty').hidden = $('files').children.length > 0;
    $('more').hidden = state.next === null;
  } catch (error) {
    $('list-status').textContent = 'Could not load files. Refresh to retry.';
    throw error;
  }
}

async function upload(file) {
  if (!file || state.uploading) return;
  state.uploading = true;
  $('upload-label').textContent = 'Uploading…';
  await perform($('upload'), async () => {
    if (file.size > state.maxBytes) throw new Error(`File exceeds ${formatSize(state.maxBytes)}.`);
    await request('/files?name=' + encodeURIComponent(file.name), {
      method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file,
    });
    await loadFiles();
    notice('File uploaded.', true);
  });
  $('upload-label').textContent = 'Upload';
  $('file').value = '';
  state.uploading = false;
}

function renderFile(file) {
  const row = $('file-row').content.firstElementChild.cloneNode(true);
  const label = row.querySelector('.file-label');
  label.textContent = label.title = file.name;
  row.querySelector('.file-size').textContent = formatSize(file.size_bytes);
  row.querySelector('.file-date').textContent = new Date(file.created_at).toLocaleDateString('en', { year: 'numeric', month: 'short', day: 'numeric' });

  const download = row.querySelector('.download');
  download.href = `/api/files/${file.id}/download`;
  download.download = file.name;
  row.querySelector('.rename-action').onclick = () => openDialog('rename', file);
  row.querySelector('.delete-action').onclick = () => openDialog('delete', file);
  $('files').append(row);
}

function openDialog(kind, file) {
  state.selected = file;
  if (kind === 'rename') {
    $('rename-name').value = file.name;
    $('rename-error').hidden = true;
    $('rename-dialog').showModal();
    $('rename-name').select();
  } else {
    $('delete-name').textContent = file.name;
    $('delete-error').hidden = true;
    $('delete-dialog').showModal();
  }
}

function renameFile(event) {
  event.preventDefault();
  perform(event.currentTarget, async () => {
    await request(`/files/${state.selected.id}`, json('PATCH', { name: $('rename-name').value }));
    $('rename-dialog').close();
    await loadFiles();
    notice('File renamed.', true);
  }, $('rename-error'));
}

function deleteFile(event) {
  event.preventDefault();
  perform(event.currentTarget, async () => {
    await request(`/files/${state.selected.id}`, { method: 'DELETE' });
    $('delete-dialog').close();
    await loadFiles();
    notice('File deleted.', true);
  }, $('delete-error'));
}

/* Drag & drop */
function connectDropZone() {
  const zone = $('drop-zone');
  let depth = 0;
  zone.addEventListener('dragenter', event => {
    if (!event.dataTransfer.types.includes('Files')) return;
    event.preventDefault();
    depth++;
    zone.classList.add('drag-over');
  });
  zone.addEventListener('dragover', event => { if (event.dataTransfer.types.includes('Files')) event.preventDefault(); });
  zone.addEventListener('dragleave', () => { depth = Math.max(0, depth - 1); if (!depth) zone.classList.remove('drag-over'); });
  zone.addEventListener('drop', event => {
    event.preventDefault(); depth = 0; zone.classList.remove('drag-over');
    if (event.dataTransfer.files.length !== 1) return notice('Upload one file at a time.');
    upload(event.dataTransfer.files[0]);
  });
}

/* API + UI helpers */
async function request(path, options = {}) {
  let response;
  try { response = await fetch('/api' + path, { credentials: 'same-origin', ...options }); }
  catch { throw new Error('Connection failed. Try again.'); }
  if (response.status === 204) return null;

  const body = await response.json();
  if (response.ok) return body;
  if (response.status === 401 && !path.startsWith('/auth/')) showSignIn();
  const error = new Error(body?.error?.message || 'Request failed.');
  error.status = response.status;
  throw error;
}

const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

async function perform(target, action, errorTarget = null) {
  notice();
  if (errorTarget) errorTarget.hidden = true;
  const buttons = target.tagName === 'BUTTON' ? [target] : [...target.querySelectorAll('button')];
  buttons.forEach(button => button.disabled = true);
  target.setAttribute('aria-busy', 'true');
  try { await action(); }
  catch (error) {
    const owner = errorTarget?.closest('dialog, section');
    if (owner && !owner.hidden && (owner.tagName !== 'DIALOG' || owner.open)) {
      errorTarget.textContent = error.message;
      errorTarget.hidden = false;
    } else notice(error.message);
  } finally {
    buttons.forEach(button => button.disabled = false);
    target.removeAttribute('aria-busy');
  }
}

function notice(message = '', success = false) {
  clearTimeout(state.noticeTimer);
  $('notice-message').textContent = message;
  $('notice').classList.toggle('success', success);
  $('notice').hidden = !message;
  if (success) state.noticeTimer = setTimeout(() => $('notice').hidden = true, 5000);
}

function formatSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1048576) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1048576).toFixed(1)} MiB`;
}

start();
