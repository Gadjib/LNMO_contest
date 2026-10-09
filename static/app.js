"use strict";
const contestClasses = document.querySelector('[data-contest-classes]');
if (contestClasses) {
  const audience = document.querySelectorAll('[name="audience"]');
  const update = () => {
    const restricted = document.querySelector('[name="audience"]:checked')?.value === 'classes';
    contestClasses.hidden = !restricted;
    contestClasses.querySelectorAll('input').forEach(input => { input.disabled = !restricted; });
  };
  audience.forEach(input => input.addEventListener('change', update));
  update();
}
const imageUpload = document.querySelector('[data-image-upload]');
if (imageUpload) {
  const form = imageUpload.closest('form');
  const statement = form.querySelector('[name="statement"]');
  const status = form.querySelector('[data-image-status]');
  let uploading = false;
  form.addEventListener('submit', event => {
    if (uploading) { event.preventDefault(); status.textContent = 'Дождитесь загрузки картинки.'; }
  });
  imageUpload.addEventListener('change', async () => {
    const file = imageUpload.files[0];
    if (!file) return;
    if (file.size > 1024 * 1024) { status.textContent = 'Картинка должна быть не больше 1 МБ.'; imageUpload.value = ''; return; }
    uploading = true;
    imageUpload.disabled = true;
    status.textContent = 'Загрузка…';
    const data = new FormData();
    data.append('csrf', form.querySelector('[name="csrf"]').value);
    data.append('image', file);
    try {
      const response = await fetch('/teacher/images', {method: 'POST', body: data});
      if (response.redirected) throw new Error('Сессия истекла. Войдите на сайт заново.');
      if (!response.ok) throw new Error((await response.text()).trim());
      const result = await response.json();
      statement.setRangeText(`\n![Описание картинки](${result.url})\n`, statement.selectionStart, statement.selectionEnd, 'end');
      statement.dispatchEvent(new Event('input', {bubbles: true}));
      statement.focus();
      status.textContent = 'Картинка вставлена. Сохраните задачу.';
    } catch (error) {
      status.textContent = error.message || 'Не удалось загрузить картинку.';
    } finally {
      uploading = false;
      imageUpload.disabled = false;
      imageUpload.value = '';
    }
  });
}
const userRole = document.querySelector('select[name="role"]');
if (userRole) {
  const field = document.querySelector('[data-class-field]');
  const updateClass = () => { if (field) { field.hidden = userRole.value !== 'student'; field.querySelector('input').disabled = field.hidden; } };
  userRole.addEventListener('change', updateClass);
  updateClass();
}
document.addEventListener("click", (event) => {
  const add = event.target.closest("[data-add]");
  if (add) {
    const kind = add.dataset.add;
    const rows = document.getElementById(`${kind}-rows`);
    const limit = kind === "test" ? 100 : 20;
    if (rows.children.length >= limit) { window.alert(`Не больше ${limit} элементов.`); return; }
    const fragment = document.getElementById(`${kind}-template`).content.cloneNode(true);
    if (kind === "test") {
      const existing = new Set([...rows.querySelectorAll('[name="test_id"]')].map(e => e.value));
      let n = 1; while (existing.has(String(n).padStart(2, "0"))) n++;
      fragment.querySelector('[name="test_id"]').value = String(n).padStart(2, "0");
    }
    rows.append(fragment);
    rows.lastElementChild.querySelector("input,textarea").focus();
  }
  const remove = event.target.closest("[data-remove]");
  if (remove) remove.closest(".repeat-row").remove();
});
const polling = document.querySelector("[data-poll]");
if (polling) {
  let failures = 0;
  async function update() {
    try {
      const response = await fetch(polling.dataset.poll, {headers: {"Accept": "application/json"}});
      if (!response.ok || !response.headers.get("content-type")?.includes("application/json")) throw new Error("status unavailable");
      const result = await response.json(); failures = 0;
      if (result.status !== polling.dataset.current || (polling.dataset.similarityPending === 'true' && result.similarity_checked)) { window.location.reload(); return; }
    } catch (_) { failures++; }
    window.setTimeout(update, Math.min(15000, 2000 * (failures + 1)));
  }
  window.setTimeout(update, 2000);
}
