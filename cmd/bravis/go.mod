module github.com/AreteAcademy/bravis/cmd/bravis

go 1.25.7

require (
	github.com/spf13/cobra v1.10.2
	github.com/AreteAcademy/bravis/sdk v0.1.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)

replace github.com/AreteAcademy/bravis/sdk => ../../sdk
