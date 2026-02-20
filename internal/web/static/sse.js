// htmx SSE extension for ralph-wiggo dashboard.
// Connects to a server-sent events endpoint and swaps content into the DOM.
(function() {
  htmx.defineExtension('sse', {
    onEvent: function(name, evt) {
      if (name !== 'htmx:afterProcessNode') return;

      var elt = evt.detail.elt;
      if (!elt || !elt.getAttribute) return;

      var url = elt.getAttribute('sse-connect');
      if (url) initSSE(elt, url);
    }
  });

  function initSSE(elt, url) {
    if (elt._sse) elt._sse.close();

    var source = new EventSource(url);
    elt._sse = source;

    // Register swap targets (elements with sse-swap attribute).
    var targets = elt.querySelectorAll('[sse-swap]');
    for (var i = 0; i < targets.length; i++) {
      registerSwap(targets[i], source);
    }
    if (elt.hasAttribute('sse-swap')) registerSwap(elt, source);

    // Close connection on "done" event from the server.
    source.addEventListener('done', function() {
      source.close();
    });

    source.onerror = function() {
      source.close();
    };
  }

  function registerSwap(target, source) {
    var eventName = target.getAttribute('sse-swap');
    if (!eventName) return;

    source.addEventListener(eventName, function(e) {
      var mode = target.getAttribute('hx-swap') || 'innerHTML';
      if (mode === 'beforeend') {
        target.insertAdjacentHTML('beforeend', e.data);
      } else if (mode === 'afterbegin') {
        target.insertAdjacentHTML('afterbegin', e.data);
      } else {
        target.innerHTML = e.data;
      }
      // Let htmx process any new elements.
      if (htmx.process) htmx.process(target);
    });
  }
})();
