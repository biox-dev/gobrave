package handler

func GetDefaultIOSchame() string {
	io_schame := `

{
    "inputs": [
        {
            "label": "input1",
            "name": "input1",
            "type": "BaseInput",
            "required": true
        },
        {
            "label": "input2",
            "name": "input2",
            "type": "BaseInput",
            "required": true
        }
    ],
    "outputs": [
        {
            "name": "tsv",
            "type": "file"
        },   {
            "name": "plot",
            "type": "file"
        }
    ],
    "params": [
        {
            "name": "params_name",
            "label": "params_name",
            "type": "BaseInput",
            "initialValue": "params_value"
        }
    ],
    "resources": {
        "cpu": 4,
        "memory": "6GB"
    },
    "ui": {
        "icon": "scissors",
        "color": "green"
    }
}
	`
	return io_schame
}

func GetInitScript(scriptType string) string {
	switch scriptType {
	case "r":
		return `
# 这是一个脚本案例
library(tidyverse)
params <- jsonlite::fromJSON("params.json", simplifyVector = FALSE)
output_dir <- params$output_dir

message(params$input1)
message(params$input2)
iris_path <- file.path(output_dir,"iris.tsv")
write_tsv(iris,file =iris_path)

iris_plot <- file.path(output_dir,"iris.png")
ggplot(iris,aes(x=Species,y=Sepal.Width)) +
  geom_boxplot()
ggsave(filename = iris_plot)

output_md <- c(
  "# Output",
  "",
  sprintf("- input1: %s", params$input1),
  sprintf("- input2: %s", params$input2)
)
readr::write_lines(output_md, file.path(output_dir, "output.md"))


outputs <- list(
  tsv = iris_path,
  plot = iris_plot
)
jsonlite::write_json(outputs, file.path(output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
    `
	case "qmd":
		rCode := `# library(tidyverse)
# params <- jsonlite::fromJSON("params.json", simplifyVector = FALSE)
# output_dir <- params$output_dir
`

		// 用双引号包裹反引号，再和原始字符串拼接
		fullCode := "\n```\n" + rCode + "\n```"
		return `
---
title: "title"
format:
  md:
    variant: gfm
editor: source
execute:
  echo: false
# knitr:
#   opts_chunk:
#     cache.path: "../cache/"
# editor_options: 
#   chunk_output_type: console
---
        ` + fullCode
	case "jupyter":
		return `{
 "cells": [],
 "metadata": {},
 "nbformat": 4,
 "nbformat_minor": 5
}
`
	default:
		return `
    `
	}
}
