// Interacoes das paginas SSR. Um arquivo pequeno, carregado em toda pagina, com
// duas responsabilidades: abrir os dialogos de erro e dar vida aos graficos.
//
// Delegacao de eventos em vez de um listener por elemento: as tabelas sao
// re-renderizadas pelo servidor a cada navegacao, e listeners presos a linhas
// especificas morreriam junto com elas.
(function () {
  "use strict";

  // --- Dialogos ------------------------------------------------------------
  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    var gatilho = e.target.closest("[data-dialogo]");
    if (gatilho) {
      var d = document.getElementById(gatilho.dataset.dialogo);
      if (d && typeof d.showModal === "function") d.showModal();
      return;
    }
    // Clique no backdrop fecha. O <dialog> nao distingue backdrop de conteudo
    // sozinho: o alvo do clique E o proprio dialog quando se acerta a moldura.
    if (e.target.tagName === "DIALOG") e.target.close();
  });

  // Abrir por link: /runs#erro-<id> ja chega com o dialogo aberto. Serve para
  // mandar a falha exata para alguem em vez de "abre a lista e procura".
  function abrirPeloHash() {
    if (!location.hash) return;
    var d = document.getElementById(location.hash.slice(1));
    if (d && d.tagName === "DIALOG" && !d.open && typeof d.showModal === "function") {
      d.showModal();
    }
  }
  abrirPeloHash();
  window.addEventListener("hashchange", abrirPeloHash);

  // --- Tooltip dos graficos ------------------------------------------------
  //
  // O <title> do SVG ate mostra o valor, mas so depois de um segundo parado e
  // com a aparencia do sistema operacional. Aqui a dica aparece na hora, segue o
  // cursor e usa a mesma tipografia do resto da pagina.
  var dica = null;

  function mostrar(texto, x, y) {
    if (!dica) {
      dica = document.createElement("div");
      dica.className = "grafico-dica";
      document.body.appendChild(dica);
    }
    dica.textContent = texto;
    dica.style.display = "block";
    // Posiciona acima e a direita do cursor, virando para o outro lado quando
    // esbarra na borda — senao a dica sai da tela nas ultimas colunas.
    var largura = dica.offsetWidth;
    var esquerda = x + 14;
    if (esquerda + largura > window.innerWidth - 8) esquerda = x - largura - 14;
    dica.style.left = esquerda + "px";
    dica.style.top = y - dica.offsetHeight - 12 + "px";
  }

  function esconder() {
    if (dica) dica.style.display = "none";
  }

  document.addEventListener("mousemove", function (e) {
    // `closest` nao existe em todo alvo possivel de evento (o proprio document,
    // por exemplo). Sem a guarda, um mousemove fora de qualquer elemento lanca
    // TypeError e mata o listener para o resto da sessao.
    if (!e.target || typeof e.target.closest !== "function") return;
    var alvo = e.target.closest("[data-dica]");
    if (alvo) mostrar(alvo.dataset.dica, e.clientX, e.clientY);
    else esconder();
  });

  // Rolar com a dica aberta a deixaria flutuando sobre outro ponto do grafico.
  window.addEventListener("scroll", esconder, { passive: true });
  document.addEventListener("mouseleave", esconder);
})();
