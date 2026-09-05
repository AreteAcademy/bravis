---
title: White label
description: Changing the interface's title, motto, logo and palette through one YAML file.
group: Operations
order: 12
slug: white-label
---

The interface is customisable through a file. Colours override the CSS variables
**at runtime**, so changing a customer's theme recompiles nothing.

## The file

Copy `brand.example.yaml` to `brand.yaml` at the root, or point
`BREVIS_BRAND_FILE` at it:

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**Every field is optional.** What you leave out keeps the default, so a file
with just `titulo:` is already valid.

The line break in `frase` is preserved: it sets the rhythm of the phrase in the
sidebar.

## Logo

```yaml
logo: https://example.com/mark.svg
```

Accepts a URL (`https://` or `http://`) or an internal path starting with `/`.
Absent, it uses the built-in symbol, which follows the theme colour.

:::warning A logo hosted elsewhere depends on that host
If it goes down, or if the cluster has no internet egress, the image does not
load. The rest of the interface keeps working, but weigh that when choosing —
the rest of the UI, fonts and bundles included, is served from the binary itself
precisely so as not to depend on the network.
:::

## Validating

A wrong hex value would only surface when the container came up, and the message
would arrive through the pod log — far from whoever edited the file. Hence a
subcommand:

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

Call it in the installation's CI and the error comes back in the pull request.

:::note Absence means different things
In `serve`, a missing file means "use the default identity" — and the API comes
up. In `marca`, a missing file is an **error**: someone who asked to validate a
path expects to be told it does not exist.
:::

## Behaviour at boot

A content error — an invalid colour, broken YAML — **does not stop the interface
from coming up**. The process logs a warning and uses the default theme. Taking
the API down over a colour would be worse than serving it with the default
theme.

```
level=WARN msg="identidade visual ignorada" arquivo=brand.yaml erro="cor invalida: #gggggg"
```

## What cannot be customised

The **"Powered by Brevis"** line in the footer does not come from configuration
— it comes from the code.

## On Kubernetes

Mount the file from a ConfigMap:

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
