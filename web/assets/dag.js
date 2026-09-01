// Ilha interativa da DAG (secao 20 do plano).
//
// Escrita em JS puro, sem JSX e sem bundler, de proposito: a secao 15 proibe
// Node no build ("Tailwind standalone, sem npm"), e um passo de transpilacao so
// para esta tela traria toolchain inteira de volta. `React.createElement` e
// verboso, mas o custo fica nesta unica pagina.
//
// O React Flow NAO e fonte da verdade: posicao dos nos, arestas e estado vem
// prontos do servidor, calculados pelo mesmo `graph.Niveis` que o executor usa.
// Aqui so se desenha.
(function () {
  "use strict";

  var h = React.createElement;
  var RF = window.ReactFlow;

  // As cores sao LIDAS do CSS, nao repetidas aqui. A instalacao pode ter tema
  // proprio (ver internal/branding), e uma paleta fixa neste arquivo faria a
  // ilha da DAG ser a unica parte da tela a ignorar a customizacao.
  //
  // Resolvidas uma vez, no carregamento: getComputedStyle a cada nó re-renderizado
  // forcaria recalculo de estilo no meio do desenho do grafo.
  function tema(nome, reserva) {
    try {
      var v = getComputedStyle(document.documentElement).getPropertyValue(nome);
      return v.trim() || reserva;
    } catch (e) {
      return reserva;
    }
  }

  var TINTA = tema("--color-ink", "#21180f");
  var MUDO = tema("--color-muted", "#6e6254");
  var PAPEL = tema("--color-surface", "#fffdf8");
  var LINHA = tema("--color-line", "#21180f1a");
  var OURO = tema("--color-gold", "#aa8450");

  var CORES = {
    success: { anel: tema("--color-state-success", "#4c7a56"), rotulo: "sucesso" },
    failed: { anel: tema("--color-state-failed", "#b0503c"), rotulo: "falha" },
    running: { anel: tema("--color-state-running", "#3f6d8f"), rotulo: "executando" },
    retrying: { anel: tema("--color-state-retrying", "#a35f28"), rotulo: "repetindo" },
    queued: { anel: tema("--color-state-queued", "#b3822f"), rotulo: "na fila" },
    canceled: { anel: tema("--color-state-canceled", "#8a8175"), rotulo: "cancelado" },
    pending: { anel: tema("--color-state-pending", "#c9bfae"), rotulo: "aguardando" },
  };

  function cor(status) {
    return CORES[status] || CORES.pending;
  }

  function duracao(ms) {
    if (!ms) return "";
    if (ms < 1000) return ms + "ms";
    if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
    return Math.floor(ms / 60000) + "m" + Math.round((ms % 60000) / 1000) + "s";
  }

  // NoBravis e o card de um passo. Custom node em vez do default porque o
  // default so mostra um rotulo — e o que o operador precisa saber num incidente
  // e o estado, a duracao e se houve retry.
  function NoBravis(props) {
    var d = props.data;
    var c = cor(d.status);
    return h(
      "div",
      {
        style: {
          minWidth: 190,
          borderRadius: 14,
          border: "1px solid " + (d.status === "pending" ? LINHA : "color-mix(in srgb, " + c.anel + " 40%, transparent)"),
          background: PAPEL,
          padding: "11px 13px",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
          // `color-mix` em vez de concatenar alfa ao hexadecimal: a cor pode
          // vir de uma variavel CSS resolvida, e "var(--x)1f" nao e cor nenhuma.
          boxShadow:
            d.status === "running"
              ? "0 0 0 3px color-mix(in srgb, " + c.anel + " 14%, transparent), 0 8px 24px rgba(33,24,15,.06)"
              : "0 8px 24px rgba(33,24,15,.06)",
        },
      },
      h(RF.Handle, { type: "target", position: RF.Position.Left, style: { background: c.anel } }),
      h(
        "div",
        { style: { display: "flex", alignItems: "center", gap: 8 } },
        h("span", {
          style: {
            width: 8, height: 8, borderRadius: 9999, background: c.anel, flexShrink: 0,
          },
        }),
        h("span", { style: { color: TINTA, fontSize: 13, fontWeight: 600 } }, d.label)
      ),
      d.acao
        ? h(
            "div",
            {
              style: {
                marginTop: 4, color: MUDO, fontSize: 11,
                fontFamily: "ui-monospace, SFMono-Regular, monospace",
                overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                maxWidth: 200,
              },
            },
            d.acao
          )
        : null,
      h(
        "div",
        { style: { marginTop: 7, display: "flex", gap: 8, fontSize: 11, color: c.anel } },
        h("span", null, c.rotulo),
        d.duracao_ms ? h("span", { style: { color: MUDO } }, duracao(d.duracao_ms)) : null,
        d.tentativa ? h("span", { style: { color: "#a35f28" } }, "retry " + d.tentativa) : null
      ),
      h(RF.Handle, { type: "source", position: RF.Position.Right, style: { background: c.anel } })
    );
  }

  var TIPOS = { bravis: NoBravis };

  // Inspetor: painel lateral do no selecionado. Aparece so quando ha selecao —
  // ocupar espaco fixo com "nada selecionado" reduz a area do grafo a toa.
  function Inspetor(props) {
    var n = props.no;
    if (!n) return null;
    var d = n.data;
    var c = cor(d.status);
    var linhas = [
      ["passo", n.id],
      ["estado", c.rotulo],
      d.acao ? ["comando", d.acao] : null,
      d.duracao_ms ? ["duracao", duracao(d.duracao_ms)] : null,
      typeof d.tentativa === "number" ? ["tentativa", String(d.tentativa + 1)] : null,
      typeof d.exit_code === "number" ? ["exit code", String(d.exit_code)] : null,
    ].filter(Boolean);

    return h(
      "aside",
      {
        style: {
          position: "absolute", top: 14, right: 14, width: 310, zIndex: 5,
          background: PAPEL, backdropFilter: "blur(6px)",
          border: "1px solid " + LINHA, borderRadius: 20, padding: 16,
          boxShadow: "0 20px 60px rgba(33,24,15,.08)",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
        },
      },
      h(
        "div",
        { style: { display: "flex", justifyContent: "space-between", alignItems: "center" } },
        h(
          "span",
          {
            style: {
              color: tema("--color-gold-strong", "#8a693d"), fontSize: 11, fontWeight: 700,
              letterSpacing: "0.14em", textTransform: "uppercase",
            },
          },
          "Detalhes"
        ),
        h(
          "button",
          {
            onClick: props.fechar,
            style: { background: "none", border: "none", color: MUDO, cursor: "pointer", fontSize: 16, lineHeight: 1 },
          },
          "×"
        )
      ),
      h(
        "dl",
        { style: { marginTop: 10, fontSize: 12 } },
        linhas.map(function (l) {
          return h(
            "div",
            { key: l[0], style: { display: "flex", gap: 8, padding: "3px 0" } },
            h("dt", { style: { color: MUDO, width: 80, flexShrink: 0 } }, l[0]),
            h(
              "dd",
              {
                style: {
                  color: TINTA, margin: 0, wordBreak: "break-all",
                  fontFamily: "ui-monospace, SFMono-Regular, monospace",
                },
              },
              l[1]
            )
          );
        })
      ),
      d.erro
        ? h(
            "pre",
            {
              style: {
                marginTop: 12, padding: 10, borderRadius: 12,
                background: "#b0503c14", border: "1px solid #b0503c33",
                color: "#8f4030", fontSize: 11, whiteSpace: "pre-wrap", wordBreak: "break-word",
                maxHeight: 160, overflow: "auto",
              },
            },
            d.erro
          )
        : null
    );
  }

  function Grafo(props) {
    var estado = React.useState({ nodes: [], edges: [], carregando: true, erro: "" });
    var dados = estado[0], setDados = estado[1];
    var sel = React.useState(null);
    var selecionado = sel[0], setSelecionado = sel[1];

    React.useEffect(function () {
      var vivo = true;
      var timer = null;

      function buscar() {
        fetch(props.src, { headers: { Accept: "application/json" } })
          .then(function (r) {
            if (!r.ok) throw new Error("HTTP " + r.status);
            return r.json();
          })
          .then(function (g) {
            if (!vivo) return;
            setDados({ nodes: g.nodes || [], edges: g.edges || [], carregando: false, erro: "" });
            // Live update por polling, nao por WebSocket: o dado muda em
            // segundos, nao em milissegundos, e um GET repetido nao precisa de
            // conexao persistente nem de reconexao.
            //
            // O intervalo segue o estado. `failed` nao e terminal no dominio —
            // um retry o move para `retrying` — mas insistir de 2 em 2s numa run
            // que provavelmente esgotou as tentativas e trafego a toa; 10s ainda
            // pega o retry sem custar nada. Terminal de verdade nao consulta mais.
            var proximo = g.terminal ? 0 : g.status === "failed" ? 10000 : 2000;
            if (proximo && g.run_id) timer = setTimeout(buscar, proximo);
          })
          .catch(function (e) {
            if (!vivo) return;
            setDados(function (d) {
              return { nodes: d.nodes, edges: d.edges, carregando: false, erro: e.message };
            });
            timer = setTimeout(buscar, 5000);
          });
      }

      buscar();
      return function () {
        vivo = false;
        if (timer) clearTimeout(timer);
      };
    }, [props.src]);

    // Reaplica o estado no no selecionado a cada atualizacao: sem isto o painel
    // congelaria no instante do clique enquanto o grafo continua avancando.
    var noAtual = selecionado
      ? dados.nodes.filter(function (n) { return n.id === selecionado; })[0]
      : null;

    return h(
      "div",
      { style: { position: "relative", width: "100%", height: "100%" } },
      dados.erro
        ? h(
            "div",
            {
              style: {
                position: "absolute", top: 12, left: 12, zIndex: 6, padding: "6px 10px",
                borderRadius: 999, background: "#b0503c14", border: "1px solid #b0503c33",
                color: "#8f4030", fontSize: 12,
              },
            },
            "falha ao carregar o grafo: " + dados.erro
          )
        : null,
      h(
        RF.ReactFlow,
        {
          nodes: dados.nodes,
          edges: dados.edges,
          nodeTypes: TIPOS,
          fitView: true,
          fitViewOptions: { padding: 0.2 },
          minZoom: 0.2,
          proOptions: { hideAttribution: false },
          defaultEdgeOptions: { style: { stroke: OURO, strokeWidth: 1.4 } },
          // Visualizacao, nao edicao: arrastar no e reconectar aresta ficam
          // desligados ate a fase do editor. Pan e zoom seguem livres.
          nodesDraggable: false,
          nodesConnectable: false,
          edgesFocusable: false,
          onNodeClick: function (_, n) { setSelecionado(n.id); },
          onPaneClick: function () { setSelecionado(null); },
        },
        h(RF.Background, { color: tema("--color-state-pending", "#c9bfae"), gap: 22, size: 1.4 }),
        h(RF.Controls, { showInteractive: false })
      ),
      h(Inspetor, { no: noAtual, fechar: function () { setSelecionado(null); } })
    );
  }

  var raiz = document.getElementById("dag");
  if (raiz) {
    ReactDOM.createRoot(raiz).render(h(Grafo, { src: raiz.dataset.src }));
  }
})();
