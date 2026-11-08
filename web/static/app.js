(function () {
  'use strict';

  const storageKey = 'gophprofile.userId';
  const pollInterval = 2000;

  const $ = (id) => document.getElementById(id);
  const errorMessage = $('errorMessage');
  const uploadView = $('uploadView');
  const galleryView = $('galleryView');
  const galleryLink = $('galleryLink');
  const form = $('uploadForm');
  const userIdInput = $('userId');
  const fileInput = $('file');
  const dropZone = $('dropZone');
  const imagePreview = $('imagePreview');
  const previewImg = $('previewImg');
  const progress = $('progress');
  const progressBar = $('progressBar');
  const submitBtn = $('submitBtn');
  const gallery = $('gallery');
  const galleryTitle = $('galleryTitle');
  const galleryEmpty = $('galleryEmpty');
  const cardTemplate = $('cardTemplate');

  let timers = [];
  let previewURL = null;

  function loadUserId() {
    try {
      return localStorage.getItem(storageKey) || '';
    } catch {
      return '';
    }
  }

  function saveUserId(id) {
    try {
      localStorage.setItem(storageKey, id);
    } catch {
      /* storage unavailable */
    }
  }

  function currentUserId() {
    return userIdInput.value.trim() || loadUserId();
  }

  function showError(message) {
    errorMessage.textContent = message;
    errorMessage.classList.remove('hidden');
  }

  function hideError() {
    errorMessage.classList.add('hidden');
    errorMessage.textContent = '';
  }

  async function errorText(body) {
    try {
      const data = typeof body === 'string' ? JSON.parse(body) : await body.json();
      return data.details ? `${data.error}: ${data.details}` : data.error;
    } catch {
      return 'Request failed';
    }
  }

  function galleryPath(userId) {
    return '/web/gallery/' + encodeURIComponent(userId);
  }

  function navigate(path) {
    history.pushState(null, '', path);
    hideError();
    route();
  }

  function route() {
    timers.forEach(clearInterval);
    timers = [];
    galleryLink.href = galleryPath(currentUserId());
    const prefix = '/web/gallery/';
    const path = location.pathname;
    if (path.startsWith(prefix) && path.length > prefix.length) {
      uploadView.classList.add('hidden');
      galleryView.classList.remove('hidden');
      showGallery(decodeURIComponent(path.slice(prefix.length)));
      return;
    }
    galleryView.classList.add('hidden');
    uploadView.classList.remove('hidden');
  }

  function setPreview(file) {
    if (previewURL) {
      URL.revokeObjectURL(previewURL);
      previewURL = null;
    }
    if (!file || !file.type.startsWith('image/')) {
      imagePreview.classList.add('hidden');
      return;
    }
    previewURL = URL.createObjectURL(file);
    previewImg.src = previewURL;
    imagePreview.classList.remove('hidden');
  }

  function setProgress(percent) {
    progress.classList.toggle('hidden', percent === null);
    progressBar.style.width = (percent || 0) + '%';
  }

  function upload(userId, file) {
    const data = new FormData();
    data.append('file', file);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/v1/avatars');
    xhr.setRequestHeader('X-User-ID', userId);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) setProgress(Math.round((e.loaded / e.total) * 100));
    };
    xhr.onload = async () => {
      submitBtn.disabled = false;
      setProgress(null);
      if (xhr.status === 201) {
        form.reset();
        setPreview(null);
        navigate(galleryPath(userId));
        return;
      }
      showError(await errorText(xhr.responseText));
    };
    xhr.onerror = () => {
      submitBtn.disabled = false;
      setProgress(null);
      showError('Network error');
    };
    submitBtn.disabled = true;
    setProgress(0);
    xhr.send(data);
  }

  const statusStyles = {
    ready: ['Ready', 'bg-green-500/20 text-green-200'],
    processing: ['Processing', 'bg-blue-500/20 text-blue-200'],
    failed: ['Failed', 'bg-red-500/20 text-red-200'],
  };

  function fillCard(card, avatar) {
    const [label, style] = statusStyles[avatar.status] || [avatar.status, 'bg-white/10 text-white'];
    const status = card.querySelector('.status');
    status.textContent = label;
    status.className = 'status text-xs px-2 py-1 rounded-full ' + style;
    card.querySelector('.name').textContent = avatar.file_name || avatar.id;
    const img = card.querySelector('img');
    const placeholder = card.querySelector('.placeholder');
    if (avatar.status === 'processing') return;
    img.src = avatar.status === 'ready' ? avatar.url + '?size=300x300' : avatar.url;
    img.alt = avatar.file_name || '';
    img.classList.remove('hidden');
    placeholder.classList.add('hidden');
  }

  function poll(card, avatar) {
    const timer = setInterval(async () => {
      const res = await fetch(avatar.url + '/metadata').catch(() => null);
      if (!res) return;
      if (res.status === 404) {
        clearInterval(timer);
        card.remove();
        return;
      }
      if (!res.ok) return;
      const fresh = await res.json();
      if (fresh.status !== 'processing') {
        clearInterval(timer);
        fillCard(card, fresh);
      }
    }, pollInterval);
    timers.push(timer);
  }

  async function remove(card, avatar) {
    const userId = currentUserId();
    if (!userId) {
      showError('Enter your user ID on the upload page first');
      return;
    }
    if (!confirm('Delete this avatar?')) return;
    const res = await fetch(avatar.url, { method: 'DELETE', headers: { 'X-User-ID': userId } }).catch(() => null);
    if (!res) {
      showError('Network error');
      return;
    }
    if (res.status !== 204) {
      showError(await errorText(res));
      return;
    }
    hideError();
    card.remove();
    galleryEmpty.classList.toggle('hidden', gallery.children.length > 0);
  }

  async function showGallery(userId) {
    galleryTitle.textContent = userId;
    gallery.replaceChildren();
    galleryEmpty.classList.add('hidden');
    const res = await fetch('/api/v1/users/' + encodeURIComponent(userId) + '/avatars').catch(() => null);
    if (!res) {
      showError('Network error');
      return;
    }
    if (!res.ok) {
      showError(await errorText(res));
      return;
    }
    const avatars = await res.json();
    galleryEmpty.classList.toggle('hidden', avatars.length > 0);
    for (const avatar of avatars) {
      const card = cardTemplate.content.firstElementChild.cloneNode(true);
      fillCard(card, avatar);
      card.querySelector('.delete').addEventListener('click', () => remove(card, avatar));
      gallery.append(card);
      if (avatar.status === 'processing') poll(card, avatar);
    }
  }

  fileInput.addEventListener('change', () => setPreview(fileInput.files[0]));

  ['dragenter', 'dragover'].forEach((type) =>
    dropZone.addEventListener(type, (e) => {
      e.preventDefault();
      dropZone.classList.add('border-purple-400');
    }),
  );
  ['dragleave', 'drop'].forEach((type) =>
    dropZone.addEventListener(type, () => dropZone.classList.remove('border-purple-400')),
  );
  dropZone.addEventListener('drop', (e) => {
    e.preventDefault();
    if (!e.dataTransfer.files.length) return;
    fileInput.files = e.dataTransfer.files;
    setPreview(fileInput.files[0]);
  });

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const userId = userIdInput.value.trim();
    const file = fileInput.files[0];
    if (!userId || !file) {
      showError('Please fill in all required fields');
      return;
    }
    hideError();
    saveUserId(userId);
    upload(userId, file);
  });

  userIdInput.addEventListener('input', () => {
    galleryLink.href = galleryPath(currentUserId());
  });

  document.querySelectorAll('[data-nav]').forEach((link) =>
    link.addEventListener('click', (e) => {
      e.preventDefault();
      navigate(link.getAttribute('href'));
    }),
  );

  window.addEventListener('popstate', () => {
    hideError();
    route();
  });

  if (!userIdInput.value) userIdInput.value = loadUserId();
  route();
})();
