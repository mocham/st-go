module st-go/platform_neutral/stterminal

go 1.26.0

require (
	st-go/config v0.0.0
	st-go/limits v0.0.0
	st-go/pdfmeta v0.0.0
	st-go/platform_neutral/api v0.0.0
)

replace (
	st-go/config => ../../functionalities/config
	st-go/limits => ../../functionalities/limits
	st-go/pdfmeta => ../../functionalities/pdfmeta
	st-go/platform_neutral/api => ../api
)
