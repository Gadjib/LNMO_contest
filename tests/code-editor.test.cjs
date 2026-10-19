const {test} = require('node:test');
const assert = require('node:assert/strict');
const {highlightSource} = require('../static/code-editor.js');
const decode = html => html.replace(/<[^>]*>/g, '').replace(/&(amp|lt|gt|quot|#39);/g, (_, x) => ({amp:'&',lt:'<',gt:'>',quot:'"','#39':"'"}[x]));
test('Python, C++, Unicode, incomplete strings and markup preserve source exactly', () => {
  for (const language of ['python3','cpp17','unknown']) {
    for (const source of ['def f(x):\n\treturn "Привет" # test\n', '#include <iostream>\nint main() { std::cout << 42; }', 's = """multiple\nlines"""\n', '/* block\ncomment */\n// line', '<script>alert("x")</script>\n&', 'x = "unfinished', 'R"tag(raw\nstring)tag"', '\n\n', '']) {
      const html = highlightSource(source, language);
      assert.equal(decode(html), source);
      assert.ok(!html.includes('<script>'));
    }
  }
});
test('tokens use the selected language', () => {
  assert.match(highlightSource('def f(): # note\n return 42', 'python3'), /syntax-keyword">def/);
  assert.match(highlightSource('#include <iostream>\nint n = 42; // note', 'cpp17'), /syntax-directive/);
  assert.match(highlightSource('/* note */', 'cpp17'), /syntax-comment/);
  assert.match(highlightSource('"""hello\nworld"""', 'python3'), /syntax-string/);
  assert.ok(!highlightSource('def', 'cpp17').includes('syntax-keyword'));
});
