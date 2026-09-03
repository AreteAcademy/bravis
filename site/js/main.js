/* Bravis — landing page.
   Sem dependência, sem build. Cada bloco degrada para nada se falhar: a
   pagina e legivel e navegavel com o script desligado. */
(function () {
  'use strict';

  var root = document.documentElement;

  /* ------------------------------------------------------------- tema ---- */

  var STORE = 'bravis-theme';

  function temaAtivo() {
    var salvo = root.getAttribute('data-theme');
    if (salvo) return salvo;
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  function rotularToggle(botao) {
    var proximo = temaAtivo() === 'dark' ? 'claro' : 'escuro';
    botao.setAttribute('aria-label', 'Mudar para o tema ' + proximo);
    botao.setAttribute('title', 'Mudar para o tema ' + proximo);
  }

  var toggle = document.getElementById('theme-toggle');
  if (toggle) {
    rotularToggle(toggle);
    toggle.addEventListener('click', function () {
      var novo = temaAtivo() === 'dark' ? 'light' : 'dark';
      root.setAttribute('data-theme', novo);
      try {
        localStorage.setItem(STORE, novo);
      } catch (e) { /* modo privado: vale so para esta aba */ }
      rotularToggle(toggle);
    });
  }

  /* -------------------------------------------------------- menu mobile ---- */

  var navToggle = document.getElementById('nav-toggle');
  var nav = document.getElementById('nav');

  if (navToggle && nav) {
    navToggle.addEventListener('click', function () {
      var aberto = nav.classList.toggle('is-open');
      navToggle.setAttribute('aria-expanded', String(aberto));
      navToggle.setAttribute('aria-label', aberto ? 'Fechar menu' : 'Abrir menu');
    });

    /* Um link de ancora dentro do menu aberto: navega e fecha. */
    nav.addEventListener('click', function (e) {
      if (e.target.tagName === 'A') {
        nav.classList.remove('is-open');
        navToggle.setAttribute('aria-expanded', 'false');
        navToggle.setAttribute('aria-label', 'Abrir menu');
      }
    });
  }

  /* -------------------------------------------------------------- tabs ---- */

  /* Um grupo por [role=tablist]. Os paineis vem de aria-controls, entao a
     ordem no HTML e a unica fonte da relacao aba/painel. */
  function ligarTabs(lista) {
    var abas = Array.prototype.slice.call(lista.querySelectorAll('[role="tab"]'));
    if (abas.length < 2) return;

    function selecionar(indice, moverFoco) {
      abas.forEach(function (aba, i) {
        var ativa = i === indice;
        aba.setAttribute('aria-selected', String(ativa));
        aba.setAttribute('tabindex', ativa ? '0' : '-1');

        var painel = document.getElementById(aba.getAttribute('aria-controls'));
        if (painel) painel.hidden = !ativa;
      });
      if (moverFoco) abas[indice].focus();
    }

    abas.forEach(function (aba, i) {
      aba.setAttribute('tabindex', aba.getAttribute('aria-selected') === 'true' ? '0' : '-1');
      aba.addEventListener('click', function () {
        selecionar(i, false);
      });
      aba.addEventListener('keydown', function (e) {
        var alvo = -1;
        if (e.key === 'ArrowRight' || e.key === 'ArrowDown') alvo = (i + 1) % abas.length;
        else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') alvo = (i - 1 + abas.length) % abas.length;
        else if (e.key === 'Home') alvo = 0;
        else if (e.key === 'End') alvo = abas.length - 1;
        if (alvo < 0) return;
        e.preventDefault();
        selecionar(alvo, true);
      });
    });
  }

  document.querySelectorAll('[role="tablist"]').forEach(ligarTabs);

  /* ------------------------------------------------------------- copiar ---- */

  document.querySelectorAll('[data-copy]').forEach(function (botao) {
    var rotulo = botao.querySelector('span');
    var original = rotulo ? rotulo.textContent : '';

    botao.addEventListener('click', function () {
      var texto = botao.getAttribute('data-copy');

      function confirmar() {
        botao.classList.add('is-done');
        if (rotulo) rotulo.textContent = 'Copiado';
        window.setTimeout(function () {
          botao.classList.remove('is-done');
          if (rotulo) rotulo.textContent = original;
        }, 1800);
      }

      /* clipboard.writeText exige contexto seguro; num file:// ou http://
         simples ele rejeita, e ai vale a selecao manual do <pre> ao lado. */
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(texto).then(confirmar, function () {});
      }
    });
  });

  /* ------------------------------------------------------------ reveal ---- */

  var alvos = document.querySelectorAll('[data-reveal]');

  if (!('IntersectionObserver' in window)) {
    alvos.forEach(function (el) {
      el.classList.add('is-visible');
    });
    return;
  }

  var observador = new IntersectionObserver(function (entradas) {
    entradas.forEach(function (entrada) {
      if (!entrada.isIntersecting) return;
      entrada.target.classList.add('is-visible');
      observador.unobserve(entrada.target);
    });
  }, { threshold: 0.08, rootMargin: '0px 0px -40px 0px' });

  alvos.forEach(function (el) {
    observador.observe(el);
  });
})();
