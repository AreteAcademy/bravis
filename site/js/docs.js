/* brevis.sh — documentação.
   Sem dependência. Tudo aqui é enfeite operacional: com o script desligado a
   página continua legível, navegável e com todos os links funcionando. */
(function () {
  'use strict';

  var raiz = document.documentElement;

  /* ------------------------------------------------------ menu lateral ---- */

  var botao = document.getElementById('side-toggle');
  var lado = document.getElementById('side');

  if (botao && lado) {
    botao.addEventListener('click', function () {
      var aberto = lado.classList.toggle('is-open');
      botao.setAttribute('aria-expanded', String(aberto));
    });
  }

  /* ---------------------------------------------------- copiar código ---- */

  /* O texto vem do atributo data-code, e não do <pre>: o <pre> carrega os
     <span> do realce, e copiar dele traria a marcação junto. */
  document.querySelectorAll('.code-block').forEach(function (bloco) {
    var b = bloco.querySelector('.code-copy');
    if (!b) return;
    var original = b.textContent;
    b.addEventListener('click', function () {
      var texto = bloco.getAttribute('data-code') || '';
      if (!navigator.clipboard || !window.isSecureContext) return;
      navigator.clipboard.writeText(texto).then(function () {
        b.classList.add('is-done');
        b.textContent = b.getAttribute('data-done') || 'ok';
        setTimeout(function () {
          b.classList.remove('is-done');
          b.textContent = original;
        }, 1600);
      }, function () {});
    });
  });

  /* ------------------------------------------------------- TOC ativo ---- */

  var elos = Array.prototype.slice.call(document.querySelectorAll('.toc a'));
  if (elos.length && 'IntersectionObserver' in window) {
    var porId = {};
    var alvos = [];
    elos.forEach(function (a) {
      var alvo = document.getElementById(decodeURIComponent(a.hash.slice(1)));
      if (alvo) {
        porId[alvo.id] = a;
        alvos.push(alvo);
      }
    });

    /* rootMargin recorta a viewport para uma faixa no alto: assim o item
       marcado é o que está sendo lido, e não o que acabou de sair da tela. */
    var visiveis = new Set();
    var obs = new IntersectionObserver(function (entradas) {
      entradas.forEach(function (e) {
        if (e.isIntersecting) visiveis.add(e.target.id);
        else visiveis.delete(e.target.id);
      });
      var atual = null;
      for (var i = 0; i < alvos.length; i++) {
        if (visiveis.has(alvos[i].id)) { atual = alvos[i].id; break; }
      }
      elos.forEach(function (a) { a.classList.remove('is-active'); });
      if (atual && porId[atual]) porId[atual].classList.add('is-active');
    }, { rootMargin: '-80px 0px -70% 0px', threshold: 0 });

    alvos.forEach(function (t) { obs.observe(t); });
  }

  /* ----------------------------------------------------------- busca ---- */

  var campo = document.getElementById('q');
  var saida = document.getElementById('search-out');
  if (!campo || !saida) return;

  var idioma = (raiz.lang || 'pt').slice(0, 2);
  var indice = null;
  var carregando = false;

  function carregar() {
    if (indice || carregando) return Promise.resolve();
    carregando = true;
    return fetch('/search-index.json')
      .then(function (r) { return r.json(); })
      .then(function (dados) {
        indice = dados.filter(function (d) { return d.l === idioma; });
      })
      .catch(function () { indice = []; });
  }

  function normalizar(s) {
    return s.toLowerCase().normalize('NFD').replace(/[̀-ͯ]/g, '');
  }

  function buscar(termo) {
    var t = normalizar(termo);
    var termos = t.split(/\s+/).filter(Boolean);
    if (!termos.length) return [];
    return indice.map(function (d) {
      var titulo = normalizar(d.t);
      var corpo = normalizar(d.d + ' ' + d.c);
      var pontos = 0;
      for (var i = 0; i < termos.length; i++) {
        // O título pesa mais que o corpo: quem digita "instalação" quer a
        // página de instalação, não os oito lugares que a mencionam.
        if (titulo.indexOf(termos[i]) >= 0) pontos += 10;
        else if (corpo.indexOf(termos[i]) >= 0) pontos += 1;
        else return null;
      }
      return { d: d, p: pontos };
    }).filter(Boolean).sort(function (a, b) { return b.p - a.p; }).slice(0, 8);
  }

  function render(itens) {
    if (!itens.length) {
      saida.innerHTML = '<p class="r-none">' + (saida.getAttribute('data-none') || '—') + '</p>';
      saida.hidden = false;
      return;
    }
    saida.innerHTML = itens.map(function (r) {
      var a = document.createElement('a');
      a.href = r.d.u;
      a.innerHTML = '<span class="r-t"></span><span class="r-g"></span>';
      a.querySelector('.r-t').textContent = r.d.t;
      a.querySelector('.r-g').textContent = r.d.g;
      return a.outerHTML;
    }).join('');
    saida.hidden = false;
  }

  var atraso;
  campo.addEventListener('input', function () {
    clearTimeout(atraso);
    var termo = campo.value.trim();
    if (termo.length < 2) {
      saida.hidden = true;
      return;
    }
    atraso = setTimeout(function () {
      carregar().then(function () { render(buscar(termo)); });
    }, 120);
  });

  campo.addEventListener('focus', carregar);

  campo.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') {
      campo.value = '';
      saida.hidden = true;
      campo.blur();
    }
    if (e.key === 'ArrowDown') {
      var primeiro = saida.querySelector('a');
      if (primeiro) { e.preventDefault(); primeiro.focus(); }
    }
  });

  document.addEventListener('click', function (e) {
    if (!saida.contains(e.target) && e.target !== campo) saida.hidden = true;
  });

  /* "/" foca a busca, como em toda documentação — menos quando já se está
     digitando em algum campo. */
  document.addEventListener('keydown', function (e) {
    if (e.key !== '/' || e.metaKey || e.ctrlKey) return;
    var t = e.target.tagName;
    if (t === 'INPUT' || t === 'TEXTAREA' || e.target.isContentEditable) return;
    e.preventDefault();
    campo.focus();
  });
})();
