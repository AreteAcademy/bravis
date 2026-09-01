# Publicar a imagem

Duas imagens saem do mesmo `Dockerfile`, do mesmo binário:

| tag | base | papel |
|---|---|---|
| `daniel3843/bravis:<versao>` | distroless | API e UI. Não executa nada, então não tem shell. |
| `daniel3843/bravis:<versao>-worker` | alpine | `scheduler`, `publish`, `backfill`. **Tem shell**, porque os passos `run:` dos workflows precisam de um. |

A separação não é preciosismo: o worker roda comandos arbitrários do YAML do
cliente, e a API não. Dar shell à API seria ampliar a superfície do processo
exposto na rede pelo componente que menos precisa disso.

## Manual

```bash
docker login -u daniel3843          # token do Docker Hub, não a senha
make image                          # arquitetura local, para testar
make image-smoke                    # confere versão e shell
make image-push                     # multi-arch (amd64 + arm64), publica
```

`make image-push` cria um builder `buildx` na primeira vez. Publicar num outro
espaço não exige editar arquivo nenhum:

```bash
make image-push NAMESPACE=outra-conta
make image-push REGISTRY=us-central1-docker.pkg.dev NAMESPACE=projeto/repo
```

## Pela CI

`.github/workflows/release.yml` publica a cada tag `v*`. Precisa de dois
segredos no repositório: `DOCKERHUB_USERNAME` e `DOCKERHUB_TOKEN` (Docker Hub →
Account Settings → Personal access tokens, permissão *Read & Write*).

```bash
echo 0.2.0 > VERSION
git commit -am "release: 0.2.0"
git tag v0.2.0 && git push origin main --tags
```

A CI recusa a tag se o `VERSION` não bater com ela — senão `bravis version`
dentro da imagem mentiria sobre a tag que a publicou, e é por ela que se
rastreia um incidente.

## Por que multi-arch

`linux/amd64` e `linux/arm64`. Desenvolvimento é Apple Silicon; uma imagem só
amd64 roda no Mac por emulação — devagar, e escondendo problemas de arquitetura
até o deploy. O `Dockerfile` compila cruzado (`--platform=$BUILDPLATFORM` no
estágio de build), então nada roda sob QEMU: são segundos, não minutos.

## Verificar o que foi publicado

```bash
docker run --rm daniel3843/bravis:0.1.0 version
docker buildx imagetools inspect daniel3843/bravis:0.1.0   # confere as duas arquiteturas
```
