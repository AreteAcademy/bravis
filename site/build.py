#!/usr/bin/env python3
"""Gerador do site do brevis.sh.

Só a biblioteca padrão: o site é servido como HTML estático, e uma dependência
aqui viraria uma dependência de quem só quer corrigir um parágrafo.

    python3 build.py            # gera em site/
    python3 build.py --check    # gera num diretório temporário e compara

O que entra:  templates/*.html, i18n.json, content/<lang>/docs/*.md
O que sai:    index.html, docs/**/index.html, en/**, sitemap.xml, search-index.json
"""

import argparse
import html
import json
import os
import re
import shutil
import sys
import tempfile
from pathlib import Path

RAIZ = Path(__file__).resolve().parent
SAIDA = RAIZ
BASE_URL = "https://brevis.sh"

# O idioma padrão mora na raiz; os demais ganham prefixo. Trocar a ordem aqui
# troca quem fica em "/" — e nada mais precisa mudar.
IDIOMAS = ["pt", "en"]
PADRAO = "pt"
LOCALE = {"pt": "pt-BR", "en": "en"}

REPO = "https://github.com/AreteAcademy/brevis"


def prefixo(lang):
    return "" if lang == PADRAO else "/" + lang


# ---------------------------------------------------------------- markdown ---

# Um subconjunto deliberado: heading, parágrafo, lista, tabela, citação, cerca
# de código, regra e admonição. Nada de HTML embutido — o conteúdo é escrito
# por quem documenta, e um parser que aceita tudo é um parser que aceita erro.

LINGUAGENS = {
    "bash": [
        (r"(#[^\n]*)", "c"),
        (r"(\$\s)", "p"),
        (r"(&quot;[^&]*?&quot;|&#x27;[^&]*?&#x27;)", "s"),
        (r"(\s)(--?[a-zA-Z][\w-]*)", "f2"),
        (r"\b(brevis|go|docker|kubectl|make|curl|psql)\b", "k"),
    ],
    "yaml": [
        (r"(#[^\n]*)", "c"),
        (r"^(\s*-?\s*)([\w.-]+)(:)", "key3"),
        (r"(&quot;[^&\n]*?&quot;|&#x27;[^&\n]*?&#x27;)", "s"),
        (r"\b(true|false|null)\b", "n"),
    ],
    "go": [
        (r"(//[^\n]*)", "c"),
        (r"(&quot;[^&\n]*?&quot;)", "s"),
        (r"\b(package|import|func|return|if|else|for|range|var|const|type|struct|nil|err|defer|go|chan|map)\b", "k"),
    ],
    "json": [
        (r"(&quot;[\w_.-]+&quot;)(\s*:)", "key2"),
        (r"(&quot;[^&\n]*?&quot;)", "s"),
        (r"\b(true|false|null|\d+)\b", "n"),
    ],
    "sql": [
        (r"(--[^\n]*)", "c"),
        (r"\b(SELECT|FROM|WHERE|INSERT|INTO|VALUES|CREATE|TABLE|AS|JOIN|ON|GROUP|BY|ORDER)\b", "k"),
    ],
}


def realcar(codigo, lang):
    """Marca tokens com <span class="t-*">. Opera sobre texto já escapado."""
    if lang not in LINGUAGENS:
        return codigo
    # Placeholders impedem que um passe reescreva o que o anterior já marcou.
    guardado = []

    def guardar(txt):
        guardado.append(txt)
        return "\x00%d\x00" % (len(guardado) - 1)

    for padrao, tipo in LINGUAGENS[lang]:
        if tipo == "key3":  # chave YAML: grupo 2 é o nome, 3 é o dois-pontos
            codigo = re.sub(
                padrao,
                lambda m: m.group(1) + guardar('<span class="t-k">%s</span>' % m.group(2)) + m.group(3),
                codigo, flags=re.M)
        elif tipo == "key2":  # chave JSON
            codigo = re.sub(
                padrao,
                lambda m: guardar('<span class="t-k">%s</span>' % m.group(1)) + m.group(2),
                codigo)
        elif tipo == "f2":  # flag: grupo 1 é o espaço que a precede
            codigo = re.sub(
                padrao,
                lambda m: m.group(1) + guardar('<span class="t-n">%s</span>' % m.group(2)),
                codigo)
        else:
            classe = {"c": "t-c", "s": "t-s", "k": "t-k", "n": "t-n", "p": "t-p"}[tipo]
            codigo = re.sub(
                padrao,
                lambda m, c=classe: guardar('<span class="%s">%s</span>' % (c, m.group(1))),
                codigo)

    for i in range(len(guardado) - 1, -1, -1):
        codigo = codigo.replace("\x00%d\x00" % i, guardado[i])
    return codigo


def slug(texto):
    t = re.sub(r"<[^>]+>", "", texto).strip().lower()
    t = (t.replace("á", "a").replace("à", "a").replace("ã", "a").replace("â", "a")
          .replace("é", "e").replace("ê", "e").replace("í", "i").replace("ó", "o")
          .replace("õ", "o").replace("ô", "o").replace("ú", "u").replace("ç", "c"))
    t = re.sub(r"[^\w\s-]", "", t)
    return re.sub(r"[\s_]+", "-", t).strip("-")


def inline(txt):
    """Formatação dentro de uma linha. A ordem importa: o código vem primeiro,
    para que `**` dentro de crases não vire negrito."""
    guardado = []

    def guardar(html_):
        guardado.append(html_)
        return "\x00%d\x00" % (len(guardado) - 1)

    txt = html.escape(txt, quote=False)
    txt = re.sub(r"`([^`]+)`", lambda m: guardar("<code>%s</code>" % m.group(1)), txt)
    txt = re.sub(r"\[([^\]]+)\]\(([^)]+)\)",
                 lambda m: guardar('<a href="%s"%s>%s</a>' % (
                     m.group(2),
                     ' target="_blank" rel="noopener"' if m.group(2).startswith("http") else "",
                     m.group(1))), txt)
    txt = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", txt)
    txt = re.sub(r"(?<![\w*])\*([^*\n]+)\*(?![\w*])", r"<em>\1</em>", txt)
    # Do último para o primeiro: um placeholder externo pode conter um interno,
    # e expandir o externo primeiro é o que traz o interno de volta ao texto.
    for i in range(len(guardado) - 1, -1, -1):
        txt = txt.replace("\x00%d\x00" % i, guardado[i])
    return txt


ADMON = {"note": "nota", "warning": "atenção", "tip": "dica", "danger": "cuidado"}


def markdown(texto, rotulos):
    """Devolve (html, sumário). O sumário lista os h2/h3 para o índice lateral."""
    linhas = texto.split("\n")
    saida, sumario = [], []
    i = 0
    while i < len(linhas):
        ln = linhas[i]

        # cerca de código
        if ln.startswith("```"):
            lang = ln[3:].strip()
            corpo = []
            i += 1
            while i < len(linhas) and not linhas[i].startswith("```"):
                corpo.append(linhas[i])
                i += 1
            i += 1
            codigo = realcar(html.escape("\n".join(corpo), quote=False), lang)
            rotulo = ('<span class="code-lang">%s</span>' % html.escape(lang)) if lang else ""
            saida.append(
                '<div class="code-block" data-code="%s">'
                '<div class="code-head">%s<button class="code-copy" type="button" '
                'aria-label="%s" data-done="%s">%s</button></div>'
                '<pre><code>%s</code></pre></div>'
                % (html.escape("\n".join(corpo), quote=True), rotulo,
                   rotulos["copiar"], rotulos["copiado"], rotulos["copiar"], codigo))
            continue

        # admonição
        m = re.match(r":::(\w+)\s*(.*)", ln)
        if m and m.group(1) in ADMON:
            tipo, titulo = m.group(1), m.group(2).strip()
            corpo = []
            i += 1
            while i < len(linhas) and not linhas[i].startswith(":::"):
                corpo.append(linhas[i])
                i += 1
            i += 1
            interno, _ = markdown("\n".join(corpo), rotulos)
            saida.append('<aside class="admon admon-%s"><p class="admon-t">%s</p>%s</aside>'
                         % (tipo, html.escape(titulo or rotulos["admon"][tipo]), interno))
            continue

        # tabela
        if ln.startswith("|") and i + 1 < len(linhas) and re.match(r"^\|[\s:|-]+\|?$", linhas[i + 1]):
            cabec = [c.strip() for c in ln.strip("|").split("|")]
            i += 2
            corpo = []
            while i < len(linhas) and linhas[i].startswith("|"):
                corpo.append([c.strip() for c in linhas[i].strip("|").split("|")])
                i += 1
            th = "".join("<th>%s</th>" % inline(c) for c in cabec)
            trs = "".join("<tr>%s</tr>" % "".join("<td>%s</td>" % inline(c) for c in r) for r in corpo)
            saida.append('<div class="table-wrap"><table><thead><tr>%s</tr></thead>'
                         "<tbody>%s</tbody></table></div>" % (th, trs))
            continue

        # heading
        m = re.match(r"^(#{1,4})\s+(.*)", ln)
        if m:
            nivel, txt = len(m.group(1)), inline(m.group(2))
            ident = slug(m.group(2))
            if nivel in (2, 3):
                sumario.append({"n": nivel, "id": ident, "t": re.sub(r"<[^>]+>", "", txt)})
                saida.append('<h%d id="%s">%s<a class="anchor" href="#%s" aria-label="link">#</a></h%d>'
                             % (nivel, ident, txt, ident, nivel))
            else:
                saida.append("<h%d>%s</h%d>" % (nivel, txt, nivel))
            i += 1
            continue

        # citação
        if ln.startswith(">"):
            corpo = []
            while i < len(linhas) and linhas[i].startswith(">"):
                corpo.append(linhas[i].lstrip(">").strip())
                i += 1
            saida.append("<blockquote><p>%s</p></blockquote>" % inline(" ".join(corpo)))
            continue

        # lista
        if re.match(r"^\s*[-*]\s+", ln) or re.match(r"^\s*\d+\.\s+", ln):
            ordenada = bool(re.match(r"^\s*\d+\.\s+", ln))
            itens = []
            while i < len(linhas) and (re.match(r"^\s*[-*]\s+", linhas[i]) or re.match(r"^\s*\d+\.\s+", linhas[i])):
                itens.append(re.sub(r"^\s*(?:[-*]|\d+\.)\s+", "", linhas[i]))
                i += 1
                # continuação indentada do mesmo item
                while i < len(linhas) and linhas[i].startswith("  ") and linhas[i].strip() \
                        and not re.match(r"^\s*(?:[-*]|\d+\.)\s+", linhas[i]):
                    itens[-1] += " " + linhas[i].strip()
                    i += 1
            tag = "ol" if ordenada else "ul"
            saida.append("<%s>%s</%s>" % (tag, "".join("<li>%s</li>" % inline(x) for x in itens), tag))
            continue

        # regra
        if re.match(r"^---+$", ln):
            saida.append("<hr>")
            i += 1
            continue

        # parágrafo
        if ln.strip():
            corpo = []
            while i < len(linhas) and linhas[i].strip() and not re.match(
                    r"^(#{1,4}\s|```|\||>|---+$|:::)", linhas[i]) and not re.match(
                    r"^\s*(?:[-*]|\d+\.)\s+", linhas[i]):
                corpo.append(linhas[i].strip())
                i += 1
            saida.append("<p>%s</p>" % inline(" ".join(corpo)))
            continue

        i += 1
    return "\n".join(saida), sumario


def front_matter(texto):
    """`key: value` entre duas linhas `---` no topo. Sem YAML de verdade: o que
    o front matter precisa carregar aqui é sempre uma string de uma linha."""
    if not texto.startswith("---"):
        return {}, texto
    fim = texto.find("\n---", 3)
    if fim < 0:
        return {}, texto
    meta = {}
    for ln in texto[3:fim].strip().split("\n"):
        if ":" in ln:
            k, v = ln.split(":", 1)
            meta[k.strip()] = v.strip().strip('"')
    return meta, texto[fim + 4:].lstrip("\n")


# ---------------------------------------------------------------- templates ---

def carregar_templates():
    t = {}
    for f in (RAIZ / "templates").glob("*.html"):
        t[f.stem] = f.read_text(encoding="utf-8")
    return t


def preencher(tpl, valores):
    """Substitui {{ chave }}. Uma chave ausente é erro, não string vazia: um
    placeholder que sobrevive até o HTML é um bug que ninguém vê."""
    faltando = []

    def troca(m):
        k = m.group(1).strip()
        if k not in valores:
            faltando.append(k)
            return ""
        return str(valores[k])

    out = re.sub(r"\{\{\s*([\w.]+)\s*\}\}", troca, tpl)
    if faltando:
        raise KeyError("placeholder sem valor: %s" % ", ".join(sorted(set(faltando))))
    return out


# ------------------------------------------------------------------ geração ---

def escrever(destino, caminho, conteudo):
    alvo = destino / caminho
    alvo.parent.mkdir(parents=True, exist_ok=True)
    alvo.write_text(conteudo, encoding="utf-8")
    return caminho


def carregar_docs(lang):
    """Lê content/<lang>/docs/*.md e devolve na ordem do front matter."""
    dir_ = RAIZ / "content" / lang / "docs"
    paginas = []
    for f in sorted(dir_.glob("*.md")):
        meta, corpo = front_matter(f.read_text(encoding="utf-8"))
        paginas.append({
            "slug": meta.get("slug", f.stem),
            "titulo": meta.get("title", f.stem),
            "descricao": meta.get("description", ""),
            "grupo": meta.get("group", ""),
            "ordem": int(meta.get("order", "99")),
            "corpo": corpo,
        })
    paginas.sort(key=lambda p: p["ordem"])
    return paginas


def nav_docs(paginas, lang, atual):
    """Sidebar agrupada, na ordem em que os grupos aparecem."""
    grupos, ordem = {}, []
    for p in paginas:
        if p["grupo"] not in grupos:
            grupos[p["grupo"]] = []
            ordem.append(p["grupo"])
        grupos[p["grupo"]].append(p)
    out = []
    for g in ordem:
        out.append('<p class="side-group">%s</p><ul class="side-list">' % html.escape(g))
        for p in grupos[g]:
            href = "%s/docs/%s/" % (prefixo(lang), p["slug"])
            ativo = ' aria-current="page"' if p["slug"] == atual else ""
            out.append('<li><a href="%s"%s>%s</a></li>' % (href, ativo, html.escape(p["titulo"])))
        out.append("</ul>")
    return "".join(out)


def gerar(destino):
    tpl = carregar_templates()
    i18n = json.loads((RAIZ / "i18n.json").read_text(encoding="utf-8"))
    escritos, urls, indice = [], [], []

    for lang in IDIOMAS:
        s = i18n[lang]
        s_ui = s["ui"]
        pfx = prefixo(lang)
        alternativas = "".join(
            '<link rel="alternate" hreflang="%s" href="%s%s/">' % (LOCALE[l], BASE_URL, prefixo(l))
            for l in IDIOMAS
        ) + '<link rel="alternate" hreflang="x-default" href="%s/">' % BASE_URL

        comum = {
            "lang": LOCALE[lang],
            "prefixo": pfx,
            "repo": REPO,
            "ano": "2026",
            "alt_landing": alternativas,
        }
        comum.update({"i18n." + k: html.escape(v) if isinstance(v, str) else v
                      for k, v in s["ui"].items()})

        # troca de idioma: cada item aponta para o mesmo lugar no outro idioma
        def seletor(caminho_rel):
            itens = []
            for l in IDIOMAS:
                href = "%s/%s" % (prefixo(l), caminho_rel) if caminho_rel else (prefixo(l) + "/")
                href = re.sub(r"//+", "/", href)
                atual = ' aria-current="true"' if l == lang else ""
                itens.append('<a href="%s" hreflang="%s"%s>%s</a>' % (href, LOCALE[l], atual, l.upper()))
            return '<div class="lang-switch" role="group" aria-label="%s">%s</div>' % (
                html.escape(s["ui"]["idioma"]), "".join(itens))

        # ---- landing ----
        corpo = preencher(tpl["landing"], dict(comum, **{
            "l." + k: v for k, v in s["landing"].items()
        }))
        pagina = preencher(tpl["base"], dict(comum, **{
            "titulo": html.escape(s["landing"]["meta_title"]),
            "descricao": html.escape(s["landing"]["meta_desc"]),
            "canonical": "%s%s/" % (BASE_URL, pfx),
            "alternativas": alternativas,
            "seletor": seletor(""),
            "classe_body": "page-landing",
            "conteudo": corpo,
            "extra_css": "",
            "extra_js": '<script src="/js/main.js" defer></script>',
            "jsonld": json.dumps(jsonld(lang, s), ensure_ascii=False, indent=2),
        }))
        escritos.append(escrever(destino, ("%s/index.html" % pfx).lstrip("/"), pagina))
        urls.append(("%s%s/" % (BASE_URL, pfx), 1.0))

        # ---- índice de /docs/ ----
        # Sem esta página, /docs/ é um diretório sem index e o servidor
        # responde 403 — e é para /docs/ que o menu aponta.
        paginas = carregar_docs(lang)
        grupos, ordem_g = {}, []
        for pg in paginas:
            if pg["grupo"] not in grupos:
                grupos[pg["grupo"]] = []
                ordem_g.append(pg["grupo"])
            grupos[pg["grupo"]].append(pg)
        secoes = []
        for g in ordem_g:
            cartoes = "".join(
                '<a class="surface" href="%s/docs/%s/"><h3>%s</h3><p>%s</p></a>'
                % (pfx, pg["slug"], html.escape(pg["titulo"]), html.escape(pg["descricao"]))
                for pg in grupos[g])
            secoes.append('<h2>%s</h2><div class="surface-grid">%s</div>'
                          % (html.escape(g), cartoes))
        corpo = ('<main id="conteudo" class="doc-index"><div class="shell">'
                 '<p class="eyebrow">%s</p><h1>%s</h1><p class="lead">%s</p>%s</div></main>'
                 % (html.escape(s_ui["docs"]), html.escape(s_ui["docs_titulo"]),
                    html.escape(s_ui["docs_desc"]), "".join(secoes)))
        pagina = preencher(tpl["base"], dict(comum, **{
            "titulo": html.escape(s_ui["docs_titulo"] + " — brevis.sh"),
            "descricao": html.escape(s_ui["docs_desc"]),
            "canonical": "%s%s/docs/" % (BASE_URL, pfx),
            "alternativas": "".join(
                '<link rel="alternate" hreflang="%s" href="%s%s/docs/">' % (LOCALE[l], BASE_URL, prefixo(l))
                for l in IDIOMAS),
            "seletor": seletor("docs/"),
            "classe_body": "page-landing",
            "conteudo": corpo,
            "extra_css": '<link rel="stylesheet" href="/css/docs.css">',
            "extra_js": "",
            "jsonld": json.dumps({"@context": "https://schema.org", "@type": "CollectionPage",
                                  "name": s_ui["docs_titulo"], "inLanguage": LOCALE[lang]},
                                 ensure_ascii=False),
        }))
        escritos.append(escrever(destino, ("%s/docs/index.html" % pfx).lstrip("/"), pagina))
        urls.append(("%s%s/docs/" % (BASE_URL, pfx), 0.9))

        # ---- páginas ----
        for idx, p in enumerate(paginas):
            corpo_html, sumario = markdown(p["corpo"], s["ui"])
            # O autor escreve sempre "/docs/x/"; o idioma que não fica na raiz
            # recebe o prefixo aqui, para que a mesma frase sirva aos dois.
            if pfx:
                corpo_html = corpo_html.replace('href="/docs/', 'href="%s/docs/' % pfx)
            toc = "".join(
                '<li class="toc-%d"><a href="#%s">%s</a></li>' % (h["n"], h["id"], html.escape(h["t"]))
                for h in sumario)
            ant = paginas[idx - 1] if idx else None
            prox = paginas[idx + 1] if idx + 1 < len(paginas) else None

            def elo(pg, rotulo, classe):
                if not pg:
                    return ""
                return ('<a class="pager %s" href="%s/docs/%s/"><span>%s</span><strong>%s</strong></a>'
                        % (classe, pfx, pg["slug"], html.escape(rotulo), html.escape(pg["titulo"])))

            rel = "docs/%s/" % p["slug"]
            corpo = preencher(tpl["doc"], dict(comum, **{
                "titulo_pagina": html.escape(p["titulo"]),
                "descricao_pagina": html.escape(p["descricao"]),
                "sidebar": nav_docs(paginas, lang, p["slug"]),
                "toc": toc,
                "toc_vazio": "" if toc else " hidden",
                "conteudo_md": corpo_html,
                "anterior": elo(ant, s["ui"]["anterior"], "pager-prev"),
                "proximo": elo(prox, s["ui"]["proximo"], "pager-next"),
                "editar": "%s/blob/master/site/content/%s/docs/%s.md" % (REPO, lang, p["slug"]),
            }))
            alt_pagina = "".join(
                '<link rel="alternate" hreflang="%s" href="%s%s/%s">' % (LOCALE[l], BASE_URL, prefixo(l), rel)
                for l in IDIOMAS)
            pagina = preencher(tpl["base"], dict(comum, **{
                "titulo": html.escape("%s — %s" % (p["titulo"], s["ui"]["docs"])),
                "descricao": html.escape(p["descricao"]),
                "canonical": "%s%s/%s" % (BASE_URL, pfx, rel),
                "alternativas": alt_pagina,
                "seletor": seletor(rel),
                "classe_body": "page-doc",
                "conteudo": corpo,
                "extra_css": '<link rel="stylesheet" href="/css/docs.css">',
                "extra_js": '<script src="/js/docs.js" defer></script>',
                "jsonld": json.dumps(jsonld_doc(lang, s, p), ensure_ascii=False, indent=2),
            }))
            escritos.append(escrever(destino, "%s/%s" % (pfx.lstrip("/"), rel + "index.html") if pfx
                                     else rel + "index.html", pagina))
            urls.append(("%s%s/%s" % (BASE_URL, pfx, rel), 0.8))
            indice.append({
                "l": lang,
                "t": p["titulo"],
                "d": p["descricao"],
                "u": "%s/%s" % (pfx, rel),
                "g": p["grupo"],
                # texto puro, para a busca; sem marcação e sem repetição
                "c": re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", corpo_html))[:1400],
            })

    # ---- artefatos ----
    s = i18n[PADRAO]
    erro = ('<main id="conteudo" class="erro-404"><div class="shell">'
            '<p class="eyebrow">404</p><h1>%s</h1><p class="lead">%s</p>'
            '<div class="button-row"><a class="button button-primary" href="/">%s</a>'
            '<a class="button button-secondary" href="/docs/">%s</a></div></div></main>'
            % (html.escape(s["ui"]["erro_titulo"]), html.escape(s["ui"]["erro_texto"]),
               html.escape(s["ui"]["erro_home"]), html.escape(s["ui"]["docs"])))
    escrever(destino, "404.html", preencher(tpl["base"], {
        "lang": LOCALE[PADRAO], "prefixo": "", "repo": REPO, "ano": "2026",
        "titulo": html.escape(s["ui"]["erro_titulo"]),
        "descricao": html.escape(s["ui"]["erro_texto"]),
        "canonical": BASE_URL + "/404.html",
        "alternativas": "", "seletor": "", "classe_body": "page-landing",
        "conteudo": erro, "extra_css": "", "extra_js": "",
        "jsonld": json.dumps({"@context": "https://schema.org", "@type": "WebPage",
                              "name": s["ui"]["erro_titulo"]}, ensure_ascii=False),
        **{"i18n." + k: html.escape(v) if isinstance(v, str) else v
           for k, v in s["ui"].items()},
    }))
    escrever(destino, "search-index.json", json.dumps(indice, ensure_ascii=False, separators=(",", ":")))
    escrever(destino, "sitemap.xml",
             '<?xml version="1.0" encoding="UTF-8"?>\n'
             '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n'
             + "".join("  <url><loc>%s</loc><priority>%.1f</priority></url>\n" % (u, p) for u, p in urls)
             + "</urlset>\n")
    escrever(destino, "robots.txt", "User-agent: *\nAllow: /\n\nSitemap: %s/sitemap.xml\n" % BASE_URL)
    return escritos


def jsonld(lang, s):
    return {
        "@context": "https://schema.org",
        "@graph": [
            {"@type": "WebSite", "@id": BASE_URL + "/#site", "url": BASE_URL + "/",
             "name": "brevis.sh", "inLanguage": LOCALE[lang],
             "description": s["landing"]["meta_desc"],
             "publisher": {"@id": "https://areteacademy.com.br/#org"}},
            {"@type": "SoftwareSourceCode", "@id": BASE_URL + "/#projeto", "name": "brevis.sh",
             "description": s["landing"]["meta_desc"], "url": BASE_URL + "/",
             "codeRepository": REPO, "programmingLanguage": "Go",
             "license": "https://opensource.org/licenses/MIT", "isAccessibleForFree": True,
             "author": {"@id": "https://areteacademy.com.br/#org"}},
            {"@type": "Organization", "@id": "https://areteacademy.com.br/#org",
             "name": "Aretê Academy", "url": "https://areteacademy.com.br/"},
        ],
    }


def jsonld_doc(lang, s, p):
    return {
        "@context": "https://schema.org",
        "@type": "TechArticle",
        "headline": p["titulo"],
        "description": p["descricao"],
        "inLanguage": LOCALE[lang],
        "isPartOf": {"@type": "WebSite", "@id": BASE_URL + "/#site"},
        "author": {"@type": "Organization", "name": "Aretê Academy"},
    }


def main():
    ap = argparse.ArgumentParser(description="Gera o site do brevis.sh")
    ap.add_argument("--check", action="store_true",
                    help="gera num diretório temporário e falha se o commitado divergir")
    args = ap.parse_args()

    if args.check:
        tmp = Path(tempfile.mkdtemp())
        try:
            escritos = gerar(tmp)
            divergentes = []
            for rel in escritos:
                atual = SAIDA / rel
                novo = (tmp / rel).read_text(encoding="utf-8")
                if not atual.exists() or atual.read_text(encoding="utf-8") != novo:
                    divergentes.append(rel)
            for extra in ("search-index.json", "sitemap.xml", "robots.txt"):
                if not (SAIDA / extra).exists() or \
                        (SAIDA / extra).read_text(encoding="utf-8") != (tmp / extra).read_text(encoding="utf-8"):
                    divergentes.append(extra)
            if divergentes:
                print("desatualizado (rode `python3 build.py`):", file=sys.stderr)
                for d in divergentes:
                    print("  " + d, file=sys.stderr)
                return 1
            print("build em dia — %d páginas" % len(escritos))
            return 0
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    escritos = gerar(SAIDA)
    print("%d páginas geradas" % len(escritos))
    for e in escritos:
        print("  " + e)
    return 0


if __name__ == "__main__":
    sys.exit(main())
