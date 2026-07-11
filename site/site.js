/* lewp site — copy-to-clipboard + docs scroll-spy. No dependencies. */
(function () {
  'use strict';

  /* ---------- copy to clipboard ---------- */

  // Command text = element text minus the "$" prompt and any button label.
  function commandText(el) {
    var clone = el.cloneNode(true);
    clone.querySelectorAll('.prompt, .copy-btn').forEach(function (n) { n.remove(); });
    return clone.textContent.trim();
  }

  function addCopyButton(el, wrapClass) {
    var wrap = document.createElement('div');
    wrap.className = wrapClass;
    el.parentNode.insertBefore(wrap, el);
    wrap.appendChild(el);

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'copy-btn';
    btn.textContent = 'copy';
    btn.setAttribute('aria-label', 'Copy command to clipboard');
    btn.setAttribute('aria-live', 'polite');
    btn.addEventListener('click', function () {
      navigator.clipboard.writeText(commandText(el)).then(function () {
        btn.textContent = 'copied ✓';
        btn.classList.add('copied');
        setTimeout(function () {
          btn.textContent = 'copy';
          btn.classList.remove('copied');
        }, 2000);
      });
    });
    wrap.appendChild(btn);
  }

  if (navigator.clipboard) {
    document.querySelectorAll('.hero-cmd, .install-cmd').forEach(function (el) {
      addCopyButton(el, 'cmd-wrap');
    });

    // Docs: only <pre> blocks that are runnable commands get a button.
    // Every non-empty line must start like a shell command; output samples,
    // usage syntax, and tables stay button-free. <pre class="no-copy"> opts out.
    var CMD_LINE = /^(lewp |curl |go |bin\/|eval |export [A-Z_]+="\$\(|[A-Z_]+="\$\()/;
    document.querySelectorAll('.doc-section pre:not(.no-copy)').forEach(function (pre) {
      var lines = pre.textContent.split('\n').filter(function (l) { return l.trim() !== ''; });
      var isCommand = lines.length > 0 && lines.every(function (l) {
        if (/^\s/.test(l)) return false; // indented continuation → usage syntax, not copyable
        return CMD_LINE.test(l);
      });
      if (isCommand) {
        pre.classList.add('has-copy');
        addCopyButton(pre, 'pre-wrap cmd-wrap');
      }
    });
  }

  /* ---------- docs scroll-spy ---------- */

  var side = document.querySelector('.doc-side');
  if (!side) return;

  var links = Array.prototype.slice.call(side.querySelectorAll('a[href^="#"]'));
  var byId = {};
  links.forEach(function (a) { byId[a.getAttribute('href').slice(1)] = a; });

  var sections = Array.prototype.slice.call(document.querySelectorAll('.doc-scroll[id]'))
    .filter(function (s) { return byId[s.id]; });
  if (!sections.length) return;

  var current = null;
  function setActive(id) {
    if (current === byId[id]) return;
    if (current) current.classList.remove('active');
    current = byId[id];
    current.classList.add('active');
  }

  // The active section is the last one whose top has passed the header line.
  function update() {
    var line = 110; // sticky header + breathing room
    var active = sections[0].id;
    for (var i = 0; i < sections.length; i++) {
      if (sections[i].getBoundingClientRect().top <= line) active = sections[i].id;
    }
    setActive(active);
  }

  var ticking = false;
  window.addEventListener('scroll', function () {
    if (ticking) return;
    ticking = true;
    requestAnimationFrame(function () { update(); ticking = false; });
  }, { passive: true });
  update();
})();
