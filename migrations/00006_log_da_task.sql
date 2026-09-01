-- +goose Up
-- Guarda a saida de cada passo.
--
-- Ate aqui o log vivia so no pod, e o pod e apagado quando o passo termina.
-- Uma pipeline que falhava as 4h deixava "falha" na tela e nada mais: a saida
-- do dbt que explicava o motivo ja tinha ido embora junto com o container.
-- Foi a lacuna mais cara de operar em dev.
--
-- TEXT, e nao um arquivo em disco ou um bucket: o volume real e pequeno (uma
-- run de dbt com 60 nos rende ~25 KB de texto), o Postgres comprime TEXT longo
-- no TOAST, e guardar aqui mantem o log ao lado do estado que ele explica —
-- sem um segundo sistema para consultar, expirar e permissionar.
--
-- Quem escreve aplica um teto e marca o que cortou; ver `janela` no runner.
ALTER TABLE task_runs ADD COLUMN log TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE task_runs DROP COLUMN log;
