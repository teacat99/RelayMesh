// 首屏主题初始化：在样式与应用脚本加载前同步设置 dark 类，避免闪烁。
// 独立成文件（而不是内联在 index.html 里），这样服务端 CSP 才能使用严格的 script-src 'self'，无需放开 'unsafe-inline'。
(function () {
  try {
    var mode = localStorage.getItem('relaymesh.theme') || localStorage.getItem('portpass.theme') || 'auto';
    var prefersDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
    var isDark = mode === 'dark' || (mode === 'auto' && prefersDark);
    if (isDark) {
      document.documentElement.classList.add('dark');
    } else {
      document.documentElement.classList.remove('dark');
    }
  } catch (e) {}
})();
