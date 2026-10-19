"use strict";
// A small local lexer: source is always escaped before adding markup.
function highlightSource(source, language) {
  const python = language === 'python3';
  const keywords = new Set((python
    ? 'False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield'
    : 'alignas alignof auto bool break case catch char class const constexpr continue decltype default delete do double else enum explicit extern false float for friend if inline int long namespace new noexcept nullptr operator private protected public register return short signed sizeof static static_assert struct switch template this thread_local throw true try typedef typename union unsigned using virtual void volatile wchar_t while').split(' '));
  const builtins = new Set((python ? 'print input range len int float str list dict set tuple sum min max sorted enumerate zip map filter abs open bool super self' : 'std cin cout cerr endl string vector size_t').split(' '));
  const pattern = python
    ? /#[^\n]*|(?:[rRuUbBfF]{0,2})(?:"""[\s\S]*?(?:"""|$)|'''[\s\S]*?(?:'''|$)|"(?:\\[\s\S]|[^"\\\n])*"?|'(?:\\[\s\S]|[^'\\\n])*'?)|\b(?:0[xX][\da-fA-F_]+|0[bB][01_]+|\d[\d_]*(?:\.[\d_]*)?(?:[eE][+-]?[\d_]+)?j?)\b|[A-Za-z_]\w*/g
    : /\/\/[^\n]*|\/\*[\s\S]*?(?:\*\/|$)|^[ \t]*#[^\n]*|R"([^ ()\\\t\r\n]{0,16})\([\s\S]*?\)\1"|"(?:\\[\s\S]|[^"\\\n])*"?|'(?:\\[\s\S]|[^'\\\n])*'?|\b(?:0[xX][\da-fA-F]+|0[bB][01]+|\d[\d']*(?:\.[\d']*)?(?:[eE][+-]?\d+)?)[uUlLfF]*\b|[A-Za-z_]\w*/gm;
  const escape = text => text.replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  if (!['python3','cpp17'].includes(language)) return escape(source);
  let html = '', end = 0;
  for (const match of source.matchAll(pattern)) {
    const token = match[0];
    let kind = '';
    if ((python && token.startsWith('#')) || (!python && token.startsWith('/'))) kind = 'comment';
    else if (!python && token.trimStart().startsWith('#')) kind = 'directive';
    else if (/^[rRuUbBfF]{0,2}["']/.test(token)) kind = 'string';
    else if (/^\d/.test(token)) kind = 'number';
    else if (keywords.has(token)) kind = 'keyword';
    else if (builtins.has(token)) kind = 'builtin';
    html += escape(source.slice(end, match.index));
    html += kind ? `<span class="syntax-${kind}">${escape(token)}</span>` : escape(token);
    end = match.index + token.length;
  }
  return html + escape(source.slice(end));
}

if (typeof module !== 'undefined') module.exports = {highlightSource};
if (typeof document !== 'undefined') {
  document.querySelectorAll('textarea.code-input').forEach(input => {
    const language = input.form.querySelector('[name="language"]');
    const shell = document.createElement('div');
    shell.className = 'code-editor';
    const preview = document.createElement('pre');
    preview.className = 'code-highlight';
    preview.setAttribute('aria-hidden', 'true');
    input.before(shell);
    shell.append(preview, input);
    input.wrap = 'off';
    input.autocapitalize = 'off';
    input.setAttribute('autocorrect', 'off');
    const syncScroll = () => { preview.scrollTop = input.scrollTop; preview.scrollLeft = input.scrollLeft; };
    const update = () => {
      preview.innerHTML = highlightSource(input.value, language.value) + '\n';
      syncScroll();
    };
    input.addEventListener('input', update);
    input.addEventListener('scroll', syncScroll);
    language.addEventListener('change', update);
    input.form.addEventListener('reset', () => setTimeout(update, 0));
    update();
  });
  document.querySelectorAll('pre.source[data-language]').forEach(block => {
    const source = block.textContent;
    block.innerHTML = highlightSource(source, block.dataset.language);
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = 'Скопировать код';
    const feedback = document.createElement('span');
    feedback.className = 'muted';
    feedback.setAttribute('role', 'status');
    const actions = document.createElement('div');
    actions.className = 'actions code-actions';
    actions.append(button, feedback);
    block.before(actions);
    button.addEventListener('click', async () => {
      try {
        if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(source);
        else {
          // HTTP LAN deployments do not expose the Clipboard API.
          const field = document.createElement('textarea');
          field.value = source;
          field.className = 'clipboard-buffer';
          document.body.append(field);
          try {
            field.focus({preventScroll: true}); field.select();
            if (!document.execCommand('copy')) throw new Error('copy unavailable');
          } finally { field.remove(); button.focus({preventScroll: true}); }
        }
        feedback.textContent = 'Код скопирован';
      } catch (_) { feedback.textContent = 'Не удалось скопировать. Выделите код и нажмите Ctrl+C.'; }
    });
  });
}
