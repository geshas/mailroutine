(function () {
  try {
    var t = localStorage.getItem('mailroutine.theme') || 'system'
    var dark = t === 'dark' || (t === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
    if (dark) document.documentElement.classList.add('dark')
  } catch (e) {
    /* storage unavailable: fall back to the light theme */
  }
})()
