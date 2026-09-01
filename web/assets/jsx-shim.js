// O bundle UMD do React Flow depende de `react/jsx-runtime`, que o React 18 NAO
// publica em UMD — so em ESM/CJS. Sem este shim o script do xyflow lanca
// "jsxRuntime is not defined" e a tela fica em branco.
//
// A reimplementacao e fiel: `jsx`/`jsxs` diferem do `createElement` apenas por
// receberem os filhos dentro de props e a key como terceiro argumento. Passar o
// config inteiro (com `children`) para o createElement preserva os dois — ele so
// sobrescreve `props.children` quando ha argumentos extras, que aqui nunca ha.
(function () {
  "use strict";
  function criar(tipo, props, key) {
    if (key === undefined) return React.createElement(tipo, props);
    return React.createElement(tipo, Object.assign({}, props, { key: key }));
  }
  window.jsxRuntime = {
    jsx: criar,
    jsxs: criar,
    jsxDEV: criar,
    Fragment: React.Fragment,
  };
})();
