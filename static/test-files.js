"use strict";
const testFileEditor = document.querySelector('form.editor');
if (testFileEditor) {
  let pendingUploads = 0;
  testFileEditor.addEventListener('submit', event => {
    if (pendingUploads) { event.preventDefault(); alert('Дождитесь чтения файлов тестов.'); }
  });
  function attachTestUploads() {
    testFileEditor.querySelectorAll('textarea[name="test_input"],textarea[name="test_answer"]').forEach(area => {
      if (area.dataset.uploadReady) return;
      area.dataset.uploadReady = 'true';
      const upload = document.createElement('input');
      upload.type = 'file'; upload.accept = '.txt,.bin,.in,.out';
      upload.setAttribute('aria-label', area.name === 'test_input' ? 'Загрузить входной файл' : 'Загрузить файл ответа');
      area.after(upload);
      upload.addEventListener('change', async () => {
        const file = upload.files[0]; if (!file) return;
        if (file.size > 1048576) { alert('Файл больше 1 МБ'); upload.value=''; return; }
        pendingUploads++;
        try {
          const data = await file.arrayBuffer();
          const side = area.name === 'test_input' ? 'input' : 'output';
          const binary = testFileEditor.elements['io_'+side+'_type'].value === 'file' && testFileEditor.elements['io_'+side+'_content'].value === 'binary';
          area.value = binary ? Array.from(new Uint8Array(data), b=>b.toString(16).padStart(2,'0')).join(' ') : new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(data);
          area.dispatchEvent(new Event('input',{bubbles:true}));
        } catch (_) { alert('Не удалось прочитать файл. Для .bin выберите бинарное содержимое.'); }
        finally { upload.value=''; pendingUploads--; }
      });
    });
  }
  attachTestUploads();
  new MutationObserver(attachTestUploads).observe(document.getElementById('test-rows'),{childList:true,subtree:true});
}
