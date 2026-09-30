'use strict';

// ---------- helpers ----------
async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  });
  if (res.status === 401) { location.href = '/'; return null; }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
  return data;
}

function fmtBytes(b) {
  if (b == null) return '–';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return b.toFixed(b >= 100 ? 0 : 1) + ' ' + u[i];
}

function fmtUptime(sec) {
  sec = Math.floor(sec);
  const d = Math.floor(sec / 86400); sec %= 86400;
  const h = Math.floor(sec / 3600); sec %= 3600;
  const m = Math.floor(sec / 60);
  const parts = [];
  if (d) parts.push(d + ' hari');
  if (h) parts.push(h + ' jam');
  if (m || !parts.length) parts.push(m + ' mnt');
  return parts.join(' ');
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

function barColor(pct) {
  return pct >= 90 ? 'fill crit' : pct >= 75 ? 'fill warn' : 'fill';
}

function showErr(id, msg) {
  const e = document.getElementById(id);
  if (!msg) { e.hidden = true; return; }
  e.textContent = msg;
  e.hidden = false;
}

// ---------- tabs ----------
document.querySelectorAll('.tab').forEach(t => {
  t.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
    document.querySelectorAll('.tabpane').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    document.getElementById('tab-' + t.dataset.tab).classList.add('active');
  });
});

document.getElementById('logoutBtn').addEventListener('click', async () => {
  await fetch('/api/logout', { method: 'POST' }).catch(() => {});
  location.href = '/';
});

// ---------- dashboard ----------
async function loadMetrics() {
  try {
    const m = await api('/api/metrics');
    if (!m) return;
    document.getElementById('cpuVal').textContent = m.cpu_percent.toFixed(1) + '%';
    const cb = document.getElementById('cpuBar');
    cb.style.width = m.cpu_percent + '%';
    cb.className = barColor(m.cpu_percent);

    document.getElementById('ramVal').textContent = m.mem_percent.toFixed(1) + '%';
    const rb = document.getElementById('ramBar');
    rb.style.width = m.mem_percent + '%';
    rb.className = barColor(m.mem_percent);
    document.getElementById('ramDetail').textContent =
      fmtBytes(m.mem_used_kb * 1024) + ' / ' + fmtBytes(m.mem_total_kb * 1024);

    document.getElementById('diskVal').textContent = m.disk_percent.toFixed(1) + '%';
    const db = document.getElementById('diskBar');
    db.style.width = m.disk_percent + '%';
    db.className = barColor(m.disk_percent);
    document.getElementById('diskPath').textContent = m.disk_path;
    document.getElementById('diskDetail').textContent =
      fmtBytes(m.disk_used) + ' / ' + fmtBytes(m.disk_total);

    document.getElementById('uptimeVal').textContent = fmtUptime(m.uptime_seconds);
    document.getElementById('loadVal').textContent =
      'Load: ' + m.load1.toFixed(2) + ' ' + m.load5.toFixed(2) + ' ' + m.load15.toFixed(2);
  } catch (e) { /* abaikan, coba lagi next poll */ }
}
loadMetrics();
setInterval(loadMetrics, 3000);

// ---------- services ----------
function svcRow(s) {
  const row = el('div', 'row');
  row.appendChild(el('span', 'badge ' + (s.active ? 'on' : 'off'), s.active ? '● aktif' : '○ mati'));
  row.appendChild(el('span', 'name', s.name));
  const meta = el('span', 'meta', s.enabled ? 'auto-start' : 'manual');
  row.appendChild(meta);
  const acts = el('span', 'actions');
  const mk = (label, action, danger) => {
    const b = el('button', 'btn sm' + (danger ? ' danger' : ''), label);
    b.addEventListener('click', () => svcAction(s.name, action, b));
    acts.appendChild(b);
  };
  mk('▶', 'start');
  mk('↻', 'restart', true);
  mk('⏹', 'stop', true);
  row.appendChild(acts);
  return row;
}

async function svcAction(name, action, btn) {
  if ((action === 'stop' || action === 'restart') && !confirm(action + ' service ' + name + '?')) return;
  btn.disabled = true;
  showErr('svcError', null);
  try {
    await api('/api/services/action', {
      method: 'POST',
      body: JSON.stringify({ name, action }),
    });
    await loadServices();
  } catch (e) {
    showErr('svcError', e.message);
    btn.disabled = false;
  }
}

async function loadServices() {
  const list = document.getElementById('svcList');
  showErr('svcError', null);
  try {
    const data = await api('/api/services');
    if (!data) return;
    list.innerHTML = '';
    if (!data.services.length) list.appendChild(el('p', 'muted', 'Tidak ada service.'));
    data.services.forEach(s => list.appendChild(svcRow(s)));
  } catch (e) {
    list.innerHTML = '';
    showErr('svcError', e.message);
  }
}
document.getElementById('svcRefresh').addEventListener('click', loadServices);
loadServices();

// ---------- files ----------
let curPath = '';

function joinPath(a, b) { return a ? a + '/' + b : b; }
function parentPath(p) {
  const i = p.lastIndexOf('/');
  return i < 0 ? '' : p.slice(0, i);
}

function fileRow(e) {
  const row = el('div', 'row');
  row.appendChild(el('span', 'icon', e.is_dir ? '📁' : '📄'));
  const name = el('span', e.is_dir ? 'name dirlink' : 'name filelink', e.name);
  name.addEventListener('click', () => {
    if (e.is_dir) loadFiles(joinPath(curPath, e.name));
    else openEditor(joinPath(curPath, e.name));
  });
  row.appendChild(name);
  row.appendChild(el('span', 'meta', e.is_dir ? '' : fmtBytes(e.size)));
  const acts = el('span', 'actions');

  const dl = el('button', 'btn sm', '⬇');
  dl.title = 'Download';
  dl.addEventListener('click', () => {
    window.open('/api/files/download?path=' + encodeURIComponent(joinPath(curPath, e.name)), '_blank');
  });

  const rn = el('button', 'btn sm', '✎');
  rn.title = 'Rename';
  rn.addEventListener('click', async () => {
    const nn = prompt('Nama baru:', e.name);
    if (!nn || nn === e.name) return;
    try {
      await api('/api/files/rename', {
        method: 'POST',
        body: JSON.stringify({ old: joinPath(curPath, e.name), new: joinPath(curPath, nn) }),
      });
      loadFiles(curPath);
    } catch (ex) { showErr('fileError', ex.message); }
  });

  const del = el('button', 'btn sm danger', '🗑');
  del.title = 'Hapus';
  del.addEventListener('click', async () => {
    if (!confirm('Hapus "' + e.name + '"?')) return;
    try {
      await api('/api/files/delete', {
        method: 'POST',
        body: JSON.stringify({ path: joinPath(curPath, e.name) }),
      });
      loadFiles(curPath);
    } catch (ex) { showErr('fileError', ex.message); }
  });

  acts.appendChild(dl); acts.appendChild(rn); acts.appendChild(del);
  row.appendChild(acts);
  return row;
}

async function loadFiles(path) {
  curPath = path || '';
  const list = document.getElementById('fileList');
  document.getElementById('crumb').textContent = '/' + curPath;
  showErr('fileError', null);
  try {
    const data = await api('/api/files?path=' + encodeURIComponent(curPath));
    if (!data) return;
    list.innerHTML = '';
    if (!data.entries.length) list.appendChild(el('p', 'muted', 'Kosong.'));
    data.entries.forEach(e => list.appendChild(fileRow(e)));
  } catch (e) {
    list.innerHTML = '';
    showErr('fileError', e.message);
  }
}

document.getElementById('fileUp').addEventListener('click', () => loadFiles(parentPath(curPath)));
document.getElementById('fileRefresh').addEventListener('click', () => loadFiles(curPath));

document.getElementById('fileNew').addEventListener('click', () => {
  const n = prompt('Nama file baru:');
  if (n) openEditor(joinPath(curPath, n), '');
});

document.getElementById('fileMkdir').addEventListener('click', async () => {
  const n = prompt('Nama folder baru:');
  if (!n) return;
  try {
    await api('/api/files/mkdir', {
      method: 'POST',
      body: JSON.stringify({ path: joinPath(curPath, n) }),
    });
    loadFiles(curPath);
  } catch (e) { showErr('fileError', e.message); }
});

document.getElementById('fileUploadBtn').addEventListener('click', () =>
  document.getElementById('fileUpload').click());
document.getElementById('fileUpload').addEventListener('change', async (ev) => {
  const f = ev.target.files[0];
  if (!f) return;
  ev.target.value = '';
  const fd = new FormData();
  fd.append('file', f);
  try {
    const res = await fetch('/api/files/upload?path=' + encodeURIComponent(curPath), {
      method: 'POST', body: fd,
    });
    if (res.status === 401) { location.href = '/'; return; }
    if (!res.ok) {
      const d = await res.json().catch(() => ({}));
      throw new Error(d.error || 'Upload gagal');
    }
    loadFiles(curPath);
  } catch (e) { showErr('fileError', e.message); }
});

loadFiles('');

// ---------- editor modal ----------
let editingPath = '';

function openEditor(path, preset) {
  editingPath = path;
  document.getElementById('editorTitle').textContent = path;
  document.getElementById('editorArea').value = preset != null ? preset : 'Memuat…';
  showErr('editorError', null);
  document.getElementById('editorModal').hidden = false;
  if (preset == null) {
    api('/api/files/content?path=' + encodeURIComponent(path))
      .then(d => { if (d) document.getElementById('editorArea').value = d.content; })
      .catch(e => showErr('editorError', e.message));
  }
}

function closeEditor() {
  document.getElementById('editorModal').hidden = true;
  editingPath = '';
}

document.getElementById('editorClose').addEventListener('click', closeEditor);

// ---------- websites ----------
function fmtDate(iso) {
  if (!iso) return '–';
  const d = new Date(iso);
  return d.toLocaleDateString('id-ID', { day: 'numeric', month: 'short', year: 'numeric' });
}

function siteRow(s) {
  const row = el('div', 'row');
  const badge = s.cert_issued
    ? el('span', 'badge on', '🔒 SSL aktif')
    : el('span', 'badge off', '⏳ belum ada sertifikat');
  row.appendChild(badge);
  row.appendChild(el('span', 'name', s.domain));
  const isProxy = s.target_type === 'proxy';
  const target = isProxy
    ? '🔀 proxy → 127.0.0.1:' + s.proxy_port
    : '📁 ' + s.root;
  const meta = s.cert_issued && s.cert_expires
    ? 'exp ' + fmtDate(s.cert_expires) + ' · ' + target
    : target;
  row.appendChild(el('span', 'meta', meta));
  const acts = el('span', 'actions');

  const open = el('a', 'btn sm', '↗');
  open.title = 'Buka https://' + s.domain;
  open.href = 'https://' + s.domain;
  open.target = '_blank';
  open.rel = 'noopener';
  acts.appendChild(open);

  const del = el('button', 'btn sm danger', '🗑');
  del.title = 'Hapus';
  del.addEventListener('click', async () => {
    const extra = isProxy ? '' : ' (file di document root TIDAK dihapus)';
    if (!confirm('Hapus website "' + s.domain + '"?' + extra)) return;
    try {
      await api('/api/websites?domain=' + encodeURIComponent(s.domain), { method: 'DELETE' });
      loadSites();
    } catch (e) { showErr('siteError', e.message); }
  });
  acts.appendChild(del);

  row.appendChild(acts);
  return row;
}

async function loadSites() {
  const list = document.getElementById('siteList');
  showErr('siteError', null);
  try {
    const data = await api('/api/websites');
    if (!data) return;
    list.innerHTML = '';
    if (!data.websites.length) list.appendChild(el('p', 'muted', 'Belum ada website. Tambahkan domain di atas.'));
    data.websites.forEach(s => list.appendChild(siteRow(s)));
  } catch (e) {
    list.innerHTML = '';
    showErr('siteError', e.message);
  }
}

async function addSite() {
  const domainEl = document.getElementById('siteDomain');
  const rootEl = document.getElementById('siteRoot');
  const typeEl = document.getElementById('siteType');
  const portEl = document.getElementById('sitePort');
  const btn = document.getElementById('siteAdd');
  const domain = domainEl.value.trim();
  if (!domain) { showErr('siteError', 'Isi nama domain dulu.'); return; }
  const isProxy = typeEl.value === 'proxy';
  const proxyPort = parseInt(portEl.value, 10) || 0;
  if (isProxy && (proxyPort < 1 || proxyPort > 65535)) {
    showErr('siteError', 'Isi port aplikasi (1-65535) untuk tipe proxy.');
    return;
  }
  btn.disabled = true;
  showErr('siteError', null);
  try {
    await api('/api/websites', {
      method: 'POST',
      body: JSON.stringify({
        domain,
        root: rootEl.value.trim(),
        target_type: isProxy ? 'proxy' : 'static',
        proxy_port: proxyPort,
      }),
    });
    domainEl.value = '';
    rootEl.value = '';
    portEl.value = '';
    loadSites();
    loadBackupDatalists();
  } catch (e) {
    showErr('siteError', e.message);
  }
  btn.disabled = false;
}

document.getElementById('siteType').addEventListener('change', (e) => {
  const isProxy = e.target.value === 'proxy';
  document.getElementById('siteRoot').hidden = isProxy;
  document.getElementById('sitePort').hidden = !isProxy;
});

document.getElementById('siteAdd').addEventListener('click', addSite);
document.getElementById('siteDomain').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') addSite();
});
document.getElementById('siteRefresh').addEventListener('click', loadSites);
loadSites();

// ---------- databases ----------
let dbConfigured = false;

async function dbStatus() {
  try {
    const s = await api('/api/db/status');
    if (!s) return;
    dbConfigured = s.configured && s.reachable;
    document.getElementById('dbSetup').hidden = dbConfigured;
    document.getElementById('dbMain').hidden = !dbConfigured;
    if (dbConfigured) {
      document.getElementById('dbAddr').textContent = '⚙ ' + s.addr;
      loadDatabases();
      loadDbUsers();
    } else if (s.configured && !s.reachable) {
      document.getElementById('dbSetup').hidden = false;
      document.getElementById('dbMain').hidden = true;
      showErr('dbSetupError', 'Tidak bisa terhubung ke ' + s.addr + ': ' + (s.error || 'unknown error') +
        '. Cek apakah MySQL/MariaDB berjalan dan kredensial benar.');
    } else {
      showErr('dbSetupError', null);
    }
  } catch (e) { /* abaikan */ }
}

function dbRow(name, isSystem) {
  const row = el('div', 'row');
  row.appendChild(el('span', 'icon', isSystem ? '🔧' : '🗄️'));
  row.appendChild(el('span', 'name', name));
  row.appendChild(el('span', 'meta', isSystem ? 'sistem' : ''));
  const acts = el('span', 'actions');

  const tables = el('button', 'btn sm', '▦ tabel');
  tables.title = 'Lihat tabel';
  tables.addEventListener('click', async () => {
    try {
      const d = await api('/api/databases/tables?name=' + encodeURIComponent(name));
      alert('Tabel di "' + name + '":\n' + (d.tables.length ? d.tables.join('\n') : '(kosong)'));
    } catch (e) { showErr('dbError', e.message); }
  });
  acts.appendChild(tables);

  if (!isSystem) {
    const del = el('button', 'btn sm danger', '🗑');
    del.title = 'Hapus database';
    del.addEventListener('click', async () => {
      if (!confirm('Hapus database "' + name + '" beserta SELURUH datanya?')) return;
      try {
        await api('/api/databases?name=' + encodeURIComponent(name), { method: 'DELETE' });
        loadDatabases();
      } catch (e) { showErr('dbError', e.message); }
    });
    acts.appendChild(del);
  }
  row.appendChild(acts);
  return row;
}

async function loadDatabases() {
  const list = document.getElementById('dbList');
  showErr('dbError', null);
  try {
    const data = await api('/api/databases');
    if (!data) return;
    list.innerHTML = '';
    const dbs = data.databases || [];
    const sys = data.system || [];
    if (!dbs.length && !sys.length) list.appendChild(el('p', 'muted', 'Belum ada database.'));
    dbs.forEach(n => list.appendChild(dbRow(n, false)));
    sys.forEach(n => list.appendChild(dbRow(n, true)));
  } catch (e) {
    list.innerHTML = '';
    showErr('dbError', e.message);
  }
}

async function addDatabase() {
  const nameEl = document.getElementById('dbName');
  const btn = document.getElementById('dbAdd');
  const name = nameEl.value.trim();
  if (!name) { showErr('dbError', 'Isi nama database dulu.'); return; }
  btn.disabled = true;
  showErr('dbError', null);
  try {
    await api('/api/databases', { method: 'POST', body: JSON.stringify({ name }) });
    nameEl.value = '';
    loadDatabases();
  } catch (e) { showErr('dbError', e.message); }
  btn.disabled = false;
}

function dbUserRow(u) {
  const row = el('div', 'row');
  row.appendChild(el('span', 'icon', '👤'));
  row.appendChild(el('span', 'name', u.user + '@' + u.host));
  row.appendChild(el('span', 'meta', ''));
  const acts = el('span', 'actions');

  const pw = el('button', 'btn sm', '🔑');
  pw.title = 'Ganti password';
  pw.addEventListener('click', async () => {
    const np = prompt('Password baru untuk ' + u.user + '@' + u.host + ':');
    if (!np) return;
    try {
      await api('/api/dbusers/password', {
        method: 'POST',
        body: JSON.stringify({ user: u.user, host: u.host, password: np }),
      });
      alert('Password diganti.');
    } catch (e) { showErr('dbUserError', e.message); }
  });
  acts.appendChild(pw);

  const del = el('button', 'btn sm danger', '🗑');
  del.title = 'Hapus user';
  del.addEventListener('click', async () => {
    if (!confirm('Hapus user "' + u.user + '@' + u.host + '"?')) return;
    try {
      await api('/api/dbusers?user=' + encodeURIComponent(u.user) + '&host=' + encodeURIComponent(u.host),
        { method: 'DELETE' });
      loadDbUsers();
    } catch (e) { showErr('dbUserError', e.message); }
  });
  acts.appendChild(del);

  row.appendChild(acts);
  return row;
}

async function loadDbUsers() {
  const list = document.getElementById('dbUserList');
  showErr('dbUserError', null);
  try {
    const data = await api('/api/dbusers');
    if (!data) return;
    list.innerHTML = '';
    const users = data.users || [];
    if (!users.length) list.appendChild(el('p', 'muted', 'Belum ada user.'));
    users.forEach(u => list.appendChild(dbUserRow(u)));
  } catch (e) {
    list.innerHTML = '';
    showErr('dbUserError', e.message);
  }
}

async function addDbUser() {
  const userEl = document.getElementById('dbUser');
  const hostEl = document.getElementById('dbUserHost');
  const passEl = document.getElementById('dbUserPass');
  const dbEl = document.getElementById('dbUserDb');
  const btn = document.getElementById('dbUserAdd');
  const user = userEl.value.trim();
  const database = dbEl.value.trim();
  if (!user || !database) { showErr('dbUserError', 'Isi user dan database.'); return; }
  if (!passEl.value) { showErr('dbUserError', 'Isi password.'); return; }
  btn.disabled = true;
  showErr('dbUserError', null);
  try {
    await api('/api/dbusers', {
      method: 'POST',
      body: JSON.stringify({
        user,
        host: hostEl.value.trim() || 'localhost',
        password: passEl.value,
        database,
      }),
    });
    userEl.value = ''; hostEl.value = ''; passEl.value = ''; dbEl.value = '';
    loadDbUsers();
  } catch (e) { showErr('dbUserError', e.message); }
  btn.disabled = false;
}

document.getElementById('dbAdd').addEventListener('click', addDatabase);
document.getElementById('dbName').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') addDatabase();
});
document.getElementById('dbRefresh').addEventListener('click', dbStatus);
document.getElementById('dbSetupRefresh').addEventListener('click', dbStatus);
document.getElementById('dbUserAdd').addEventListener('click', addDbUser);
dbStatus();
document.getElementById('editorCancel').addEventListener('click', closeEditor);
document.getElementById('editorSave').addEventListener('click', async () => {
  const btn = document.getElementById('editorSave');
  btn.disabled = true;
  showErr('editorError', null);
  try {
    await api('/api/files/content', {
      method: 'PUT',
      body: JSON.stringify({ path: editingPath, content: document.getElementById('editorArea').value }),
    });
    closeEditor();
    loadFiles(curPath);
  } catch (e) {
    showErr('editorError', e.message);
  }
  btn.disabled = false;
});

// ---------- backups ----------
let bkJobs = [];
let bkEditingId = null;
let bkSelectedJobId = null;

const BK_PRESETS = ['0 2 * * *', '0 2 * * 0', '0 2 1 * *', '0 * * * *'];

function fmtDateTime(iso) {
  if (!iso) return '–';
  return new Date(iso).toLocaleString('id-ID', {
    day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit',
  });
}

function bkTargetText(j) {
  if (j.target_type === 'website') return '🌐 ' + j.domain;
  if (j.target_type === 'database') return '🗄️ ' + j.database;
  return '🌐 ' + j.domain + ' + 🗄️ ' + j.database;
}

function bkStatusBadge(j) {
  if (!j.last_status) return el('span', 'badge off', 'belum pernah jalan');
  if (j.last_status === 'running') return el('span', 'badge off', '⏳ berjalan…');
  if (j.last_status === 'ok') return el('span', 'badge on', '✓ ok');
  return el('span', 'badge off', j.last_status);
}

function bkRow(j) {
  const row = el('div', 'row');

  const sw = el('label', 'switch');
  sw.title = j.enabled ? 'Aktif — klik untuk menonaktifkan jadwal' : 'Nonaktif — klik untuk mengaktifkan jadwal';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = j.enabled;
  cb.addEventListener('change', () => toggleBackup(j, cb));
  sw.appendChild(cb);
  sw.appendChild(el('span', 'track'));
  row.appendChild(sw);

  const nameBox = el('span', 'name');
  nameBox.appendChild(document.createTextNode(j.name + ' '));
  nameBox.appendChild(bkStatusBadge(j));
  row.appendChild(nameBox);

  const last = j.last_run ? 'terakhir ' + fmtDateTime(j.last_run) : 'belum pernah jalan';
  row.appendChild(el('span', 'meta', bkTargetText(j) + ' · ' + j.schedule + ' · ' + last));

  const acts = el('span', 'actions');
  const mk = (label, title, fn, danger) => {
    const b = el('button', 'btn sm' + (danger ? ' danger' : ''), label);
    b.title = title;
    b.addEventListener('click', () => fn(b));
    acts.appendChild(b);
    return b;
  };
  mk('▶ Jalan', 'Jalankan backup sekarang', async (b) => {
    b.disabled = true;
    showErr('bkError', null);
    try {
      await api('/api/backups/jobs/run?id=' + encodeURIComponent(j.id), { method: 'POST' });
      setTimeout(loadBackups, 600);
      setTimeout(loadBackups, 2500);
    } catch (e) { showErr('bkError', e.message); b.disabled = false; }
  });
  mk('📂', 'Lihat file backup', () => loadBackupFiles(j));
  mk('✎', 'Edit job', () => fillBackupForm(j));
  mk('🗑', 'Hapus job', async () => {
    if (!confirm('Hapus job backup "' + j.name + '"? File backup yang sudah ada TIDAK ikut terhapus.')) return;
    try {
      await api('/api/backups/jobs?id=' + encodeURIComponent(j.id), { method: 'DELETE' });
      if (bkSelectedJobId === j.id) {
        bkSelectedJobId = null;
        document.getElementById('bkFilesTitle').textContent = '';
        document.getElementById('bkFileList').innerHTML = '<p class="muted">Pilih tombol 📂 pada job untuk melihat file backup-nya.</p>';
      }
      if (bkEditingId === j.id) resetBackupForm();
      loadBackups();
    } catch (e) { showErr('bkError', e.message); }
  }, true);
  row.appendChild(acts);
  return row;
}

async function toggleBackup(j, cb) {
  cb.disabled = true;
  showErr('bkError', null);
  try {
    await api('/api/backups/jobs?id=' + encodeURIComponent(j.id), {
      method: 'PUT',
      body: JSON.stringify({ ...j, enabled: cb.checked }),
    });
    loadBackups();
  } catch (e) {
    showErr('bkError', e.message);
    cb.checked = !cb.checked;
    cb.disabled = false;
  }
}

async function loadBackups() {
  const list = document.getElementById('bkJobList');
  showErr('bkError', null);
  try {
    const data = await api('/api/backups/jobs');
    if (!data) return;
    bkJobs = data.jobs || [];
    list.innerHTML = '';
    if (!bkJobs.length) list.appendChild(el('p', 'muted', 'Belum ada job backup. Buat lewat form di bawah.'));
    bkJobs.forEach(j => list.appendChild(bkRow(j)));
  } catch (e) {
    list.innerHTML = '';
    showErr('bkError', e.message);
  }
  loadBackupDatalists();
}

async function loadBackupDatalists() {
  try {
    const s = await api('/api/websites');
    if (s) {
      const dl = document.getElementById('bkDomainList');
      dl.innerHTML = '';
      (s.websites || []).forEach(x => {
        // Website tipe proxy tidak punya document root — tidak bisa di-backup.
        if (x.target_type === 'proxy') return;
        const o = document.createElement('option');
        o.value = x.domain;
        dl.appendChild(o);
      });
    }
  } catch (e) { /* abaikan */ }
  try {
    const d = await api('/api/databases');
    if (d) {
      const dl = document.getElementById('bkDbList');
      dl.innerHTML = '';
      (d.databases || []).forEach(n => {
        const o = document.createElement('option');
        o.value = n;
        dl.appendChild(o);
      });
    }
  } catch (e) { /* DB belum dikonfigurasi — biarkan kosong */ }
}

// ---------- form job backup ----------
function bkSyncTypeFields() {
  const t = document.getElementById('bkType').value;
  document.getElementById('bkDomainWrap').hidden = (t === 'database');
  document.getElementById('bkDbWrap').hidden = (t === 'website');
}

function bkSyncPreset() {
  const p = document.getElementById('bkPreset').value;
  document.getElementById('bkCronWrap').hidden = (p !== 'custom');
}

function bkFormSchedule() {
  const p = document.getElementById('bkPreset').value;
  return p === 'custom' ? document.getElementById('bkSchedule').value.trim() : p;
}

function bkReadForm() {
  return {
    name: document.getElementById('bkName').value.trim(),
    enabled: document.getElementById('bkEnabled').checked,
    schedule: bkFormSchedule(),
    target_type: document.getElementById('bkType').value,
    domain: document.getElementById('bkDomain').value.trim(),
    database: document.getElementById('bkDatabase').value.trim(),
    retention: parseInt(document.getElementById('bkRetention').value, 10) || 7,
  };
}

function fillBackupForm(j) {
  bkEditingId = j.id;
  document.getElementById('bkFormTitle').textContent = 'Edit job backup';
  document.getElementById('bkName').value = j.name;
  document.getElementById('bkType').value = j.target_type;
  document.getElementById('bkDomain').value = j.domain || '';
  document.getElementById('bkDatabase').value = j.database || '';
  if (BK_PRESETS.includes(j.schedule)) {
    document.getElementById('bkPreset').value = j.schedule;
    document.getElementById('bkSchedule').value = '';
  } else {
    document.getElementById('bkPreset').value = 'custom';
    document.getElementById('bkSchedule').value = j.schedule;
  }
  document.getElementById('bkRetention').value = j.retention;
  document.getElementById('bkEnabled').checked = j.enabled;
  document.getElementById('bkSave').textContent = '💾 Simpan perubahan';
  document.getElementById('bkCancelEdit').hidden = false;
  showErr('bkFormError', null);
  bkSyncTypeFields();
  bkSyncPreset();
  document.getElementById('bkName').focus();
}

function resetBackupForm() {
  bkEditingId = null;
  document.getElementById('bkFormTitle').textContent = 'Tambah job backup';
  ['bkName', 'bkDomain', 'bkDatabase', 'bkSchedule'].forEach(id => {
    document.getElementById(id).value = '';
  });
  document.getElementById('bkType').value = 'website';
  document.getElementById('bkPreset').value = '0 2 * * *';
  document.getElementById('bkRetention').value = '7';
  document.getElementById('bkEnabled').checked = true;
  document.getElementById('bkSave').textContent = '＋ Tambah job';
  document.getElementById('bkCancelEdit').hidden = true;
  showErr('bkFormError', null);
  bkSyncTypeFields();
  bkSyncPreset();
}

async function saveBackup() {
  const btn = document.getElementById('bkSave');
  const payload = bkReadForm();
  if (!payload.name) { showErr('bkFormError', 'Isi nama job dulu.'); return; }
  if (payload.target_type !== 'database' && !payload.domain) {
    showErr('bkFormError', 'Isi domain website.'); return;
  }
  if (payload.target_type !== 'website' && !payload.database) {
    showErr('bkFormError', 'Isi nama database.'); return;
  }
  btn.disabled = true;
  showErr('bkFormError', null);
  try {
    if (bkEditingId) {
      await api('/api/backups/jobs?id=' + encodeURIComponent(bkEditingId), {
        method: 'PUT', body: JSON.stringify(payload),
      });
    } else {
      await api('/api/backups/jobs', {
        method: 'POST', body: JSON.stringify(payload),
      });
    }
    resetBackupForm();
    loadBackups();
  } catch (e) {
    showErr('bkFormError', e.message);
  }
  btn.disabled = false;
}

// ---------- file backup ----------
function bkFileRow(job, f) {
  const row = el('div', 'row');
  row.appendChild(el('span', 'icon', f.kind === 'website' ? '🌐' : '🗄️'));
  row.appendChild(el('span', 'name', f.name));
  row.appendChild(el('span', 'meta', fmtBytes(f.size) + ' · ' + fmtDateTime(f.mod_time)));
  const acts = el('span', 'actions');

  const dl = el('button', 'btn sm', '⬇');
  dl.title = 'Download';
  dl.addEventListener('click', () => {
    window.open('/api/backups/files/download?job=' + encodeURIComponent(job.id) +
      '&file=' + encodeURIComponent(f.name), '_blank');
  });
  acts.appendChild(dl);

  const rs = el('button', 'btn sm danger', '↩ Restore');
  rs.title = 'Restore dari file ini';
  rs.addEventListener('click', async () => {
    const target = f.kind === 'website'
      ? 'document root ' + job.domain
      : 'database ' + job.database;
    if (!confirm('RESTORE backup "' + f.name + '" ke ' + target + '?\n\n' +
      'Data saat ini akan DITIMPA oleh isi backup. Aksi ini tidak bisa dibatalkan!')) return;
    if (!confirm('Benar-benar yakin? Data sekarang di ' + target + ' akan tertimpa.')) return;
    showErr('bkFileError', null);
    try {
      await api('/api/backups/files/restore', {
        method: 'POST',
        body: JSON.stringify({ job: job.id, file: f.name }),
      });
      alert('Restore selesai untuk ' + target + '.');
    } catch (e) { showErr('bkFileError', e.message); }
  });
  acts.appendChild(rs);

  const del = el('button', 'btn sm danger', '🗑');
  del.title = 'Hapus file backup';
  del.addEventListener('click', async () => {
    if (!confirm('Hapus file backup "' + f.name + '"?')) return;
    try {
      await api('/api/backups/files?job=' + encodeURIComponent(job.id) +
        '&file=' + encodeURIComponent(f.name), { method: 'DELETE' });
      loadBackupFiles(job);
    } catch (e) { showErr('bkFileError', e.message); }
  });
  acts.appendChild(del);

  row.appendChild(acts);
  return row;
}

async function loadBackupFiles(job) {
  bkSelectedJobId = job.id;
  const list = document.getElementById('bkFileList');
  document.getElementById('bkFilesTitle').textContent = '— ' + job.name;
  showErr('bkFileError', null);
  try {
    const data = await api('/api/backups/files?job=' + encodeURIComponent(job.id));
    if (!data) return;
    const files = data.files || [];
    list.innerHTML = '';
    if (!files.length) list.appendChild(el('p', 'muted', 'Belum ada file backup untuk job ini.'));
    files.forEach(f => list.appendChild(bkFileRow(job, f)));
  } catch (e) {
    list.innerHTML = '';
    showErr('bkFileError', e.message);
  }
}

document.getElementById('bkRefresh').addEventListener('click', loadBackups);
document.getElementById('bkType').addEventListener('change', bkSyncTypeFields);
document.getElementById('bkPreset').addEventListener('change', bkSyncPreset);
document.getElementById('bkSave').addEventListener('click', saveBackup);
document.getElementById('bkCancelEdit').addEventListener('click', resetBackupForm);
bkSyncTypeFields();
bkSyncPreset();
loadBackups();

// ---------- terminal ----------
let term = null, termWS = null, termFit = null, termStarted = false;

function setTermStatus(msg) {
  document.getElementById('termStatus').textContent = msg;
}

function sendTermResize() {
  if (term && termWS && termWS.readyState === WebSocket.OPEN) {
    termWS.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
  }
}

function connectTerminal() {
  if (termWS) { try { termWS.close(); } catch (e) {} termWS = null; }
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(proto + '//' + location.host + '/ws/terminal');
  ws.binaryType = 'arraybuffer';
  termWS = ws;
  setTermStatus('menyambungkan…');
  ws.onopen = () => {
    setTermStatus('tersambung');
    sendTermResize();
    term.focus();
  };
  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') term.write(ev.data);
    else term.write(new Uint8Array(ev.data));
  };
  ws.onclose = () => {
    setTermStatus('terputus — klik "Sambung ulang"');
    if (termWS === ws) { termWS = null; term.write('\r\n[ koneksi terputus ]\r\n'); }
  };
  ws.onerror = () => { setTermStatus('gagal tersambung'); };
}

function initTerminal() {
  if (termStarted) {
    if (termFit) setTimeout(() => { termFit.fit(); sendTermResize(); term.focus(); }, 30);
    if (!termWS) connectTerminal();
    return;
  }
  termStarted = true;
  term = new Terminal({ cursorBlink: true, fontSize: 14, fontFamily: 'Menlo, Consolas, monospace' });
  if (window.FitAddon && FitAddon.FitAddon) {
    termFit = new FitAddon.FitAddon();
    term.loadAddon(termFit);
  }
  term.open(document.getElementById('termBox'));
  if (termFit) termFit.fit();
  term.onData((d) => {
    if (termWS && termWS.readyState === WebSocket.OPEN) termWS.send(d);
  });
  term.onResize(() => sendTermResize());
  window.addEventListener('resize', () => { if (termFit) termFit.fit(); });
  connectTerminal();
}

document.querySelector('.tab[data-tab="terminal"]').addEventListener('click', () => {
  // Tunggu pane aktif (terlihat) agar xterm mengukur ukuran dengan benar.
  setTimeout(initTerminal, 30);
});
document.getElementById('termReconnect').addEventListener('click', () => {
  if (!termStarted) { initTerminal(); return; }
  term.clear();
  connectTerminal();
});

// ---------- apps (one-click docker) ----------
let appsCatalog = [];
let appsInstalling = null; // AppDef yang sedang diisi form install-nya

function appCard(a) {
  const card = el('div', 'card');
  card.appendChild(el('h3', null, '📦 ' + a.name));
  card.appendChild(el('p', 'muted small', a.description));
  const meta = el('p', 'muted small', 'image: ' + a.image + ' · port ' + a.container_port);
  card.appendChild(meta);
  const btn = el('button', 'btn primary', '🚀 Install');
  btn.addEventListener('click', () => openInstallForm(a));
  card.appendChild(btn);
  return card;
}

function openInstallForm(a) {
  appsInstalling = a;
  document.getElementById('appsInstallTitle').textContent = 'Install ' + a.name;
  const form = document.getElementById('appsInstallForm');
  form.innerHTML = '';
  const mkField = (id, label, opts) => {
    const lab = el('label', null, label);
    const inp = document.createElement('input');
    inp.id = id;
    if (opts.type) inp.type = opts.type;
    if (opts.value) inp.value = opts.value;
    if (opts.placeholder) inp.placeholder = opts.placeholder;
    lab.appendChild(inp);
    form.appendChild(lab);
    return inp;
  };
  mkField('appsFormDomain', 'Domain (harus sudah mengarah ke server ini)', { placeholder: 'contoh.com' });
  (a.env || []).forEach(e => {
    const label = e.label || e.key;
    mkField('appsEnv_' + e.key, label + (e.required ? ' *' : ''), {
      type: e.secret ? 'password' : 'text',
      value: e.generate ? '' : (e.default || ''),
      placeholder: e.generate ? '(kosongkan = dibuatkan otomatis)' : '',
    });
  });
  if ((a.companions || []).length) {
    form.appendChild(el('p', 'muted small',
      'Aplikasi ini otomatis dipasangkan container pendamping (' +
      a.companions.map(c => c.image).join(', ') + ') di network privat.'));
  }
  showErr('appsFormError', null);
  document.getElementById('appsInstallCard').hidden = false;
  document.getElementById('appsInstallCard').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

async function doInstall() {
  const a = appsInstalling;
  if (!a) return;
  const domain = document.getElementById('appsFormDomain').value.trim();
  if (!domain) { showErr('appsFormError', 'Isi domain dulu.'); return; }
  const env = {};
  (a.env || []).forEach(e => {
    const v = document.getElementById('appsEnv_' + e.key).value;
    if (v) env[e.key] = v;
  });
  const btn = document.getElementById('appsInstallBtn');
  btn.disabled = true;
  showErr('appsFormError', null);
  try {
    await api('/api/apps/install', {
      method: 'POST',
      body: JSON.stringify({ app_id: a.id, domain, env }),
    });
    document.getElementById('appsInstallCard').hidden = true;
    appsInstalling = null;
    loadApps();
    loadSites();
    setTimeout(loadApps, 3000);
    setTimeout(loadApps, 10000);
  } catch (e) {
    showErr('appsFormError', e.message);
  }
  btn.disabled = false;
}

function instRow(st) {
  const i = st.instance;
  const row = el('div', 'row');
  let badge;
  if (i.status === 'installing') badge = el('span', 'badge off', '⏳ meng-install…');
  else if (i.status === 'error') badge = el('span', 'badge off', '✖ error');
  else if (st.containers.length && st.running === st.containers.length) badge = el('span', 'badge on', '● jalan');
  else if (st.running > 0) badge = el('span', 'badge off', '◐ sebagian');
  else badge = el('span', 'badge off', '○ berhenti');
  row.appendChild(badge);
  row.appendChild(el('span', 'name', i.name));
  const metaParts = ['port ' + i.host_port];
  if (i.status === 'error' && i.status_error) metaParts.push(i.status_error);
  else metaParts.push(st.containers.map(c => c.name + ': ' + c.state).join(', ') || 'menunggu container');
  row.appendChild(el('span', 'meta', metaParts.join(' · ')));

  const acts = el('span', 'actions');
  const open = el('a', 'btn sm', '↗');
  open.title = 'Buka https://' + i.domain;
  open.href = 'https://' + i.domain;
  open.target = '_blank';
  open.rel = 'noopener';
  acts.appendChild(open);
  const mk = (label, title, path, danger, confirmMsg) => {
    const b = el('button', 'btn sm' + (danger ? ' danger' : ''), label);
    b.title = title;
    b.addEventListener('click', async () => {
      if (confirmMsg && !confirm(confirmMsg)) return;
      b.disabled = true;
      showErr('appsError', null);
      try {
        await api(path + '?id=' + encodeURIComponent(i.id), { method: 'POST' });
        setTimeout(loadApps, 500);
        setTimeout(loadApps, 2000);
        loadSites();
      } catch (e) { showErr('appsError', e.message); b.disabled = false; }
    });
    acts.appendChild(b);
  };
  mk('▶', 'Jalankan', '/api/apps/instances/start');
  mk('⏹', 'Berhenti', '/api/apps/instances/stop', true);
  mk('🗑', 'Uninstall (hapus container, volume & website)',
    '/api/apps/instances/uninstall', true,
    'Uninstall "' + i.name + '"? Container, volume (DATA APLIKASI), dan website proxy-nya akan dihapus permanen.');
  row.appendChild(acts);
  return row;
}

async function loadApps() {
  showErr('appsError', null);
  try {
    const data = await api('/api/apps');
    if (!data) return;
    const noDocker = document.getElementById('appsNoDocker');
    const main = document.getElementById('appsMain');
    if (!data.docker_available) {
      noDocker.hidden = false;
      main.hidden = true;
      return;
    }
    noDocker.hidden = true;
    main.hidden = false;
    appsCatalog = data.apps || [];
    const cat = document.getElementById('appsCatalog');
    cat.innerHTML = '';
    if (!appsCatalog.length) cat.appendChild(el('p', 'muted', 'Katalog kosong.'));
    appsCatalog.forEach(a => cat.appendChild(appCard(a)));

    const inst = await api('/api/apps/instances');
    if (!inst) return;
    const box = document.getElementById('appsInstances');
    box.innerHTML = '';
    if (!inst.instances.length) box.appendChild(el('p', 'muted', 'Belum ada aplikasi ter-install. Pilih dari katalog di atas.'));
    inst.instances.forEach(st => box.appendChild(instRow(st)));
  } catch (e) {
    showErr('appsError', e.message);
  }
}

document.getElementById('appsRefresh').addEventListener('click', loadApps);
document.getElementById('appsRetry').addEventListener('click', loadApps);
document.getElementById('appsInstallBtn').addEventListener('click', doInstall);
document.getElementById('appsInstallCancel').addEventListener('click', () => {
  document.getElementById('appsInstallCard').hidden = true;
  appsInstalling = null;
});
document.querySelector('.tab[data-tab="apps"]').addEventListener('click', loadApps);
loadApps();
