module st-go/term

go 1.26.0

require (
	st-go/config v0.0.0
	st-go/platform_neutral/stterminal v0.0.0
)

require (
	st-go/limits v0.0.0 // indirect
	st-go/pdfmeta v0.0.0 // indirect
	st-go/platform_neutral/api v0.0.0 // indirect
)

replace (
	st-go/config => ../functionalities/config
	st-go/limits => ../functionalities/limits
	st-go/pdfmeta => ../functionalities/pdfmeta
	st-go/platform_neutral/api => ../platform_neutral/api
	st-go/platform_neutral/stterminal => ../platform_neutral/stterminal
)
