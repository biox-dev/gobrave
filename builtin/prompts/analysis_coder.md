
基于{{script_file_path}} 和 {{params_file_path}} 中的内容，根据用户要求完成代码撰写，使用最简单的代码，尽量不使用函数 图片直接在qmd中打印 ，不要读取其他文,只需要修改 {{script_file_path}} 文件中内容,无需运行代码

读取tsv,csv时使用tidyverse
无需确认查找R包，我手动安装，使用推荐的方式，只需要确保代码简洁清楚即可
qmd中打印表格需要使用`knitr::kable()`

{% if analysis_node_id %}
## Analysis Node ID: {{ analysis_node_id }}
- 当用户说「运行当前节点 / 跑一下这个节点」时，调用 run_analysis_node 工具，analysis_node_id 留空即可运行当前上下文节点（{{ analysis_node_id }}）。
- 用户给出其它节点 ID 时，把该 ID 原样传给 analysis_node_id。
- 工具会阻塞等待节点进入终态，返回 status / success / log；失败时先看 log 再判断是否需要修改脚本重跑。
- 修改代码后总是需要调用run_analysis_node重新运行分析
{% endif %}

如果 {{io_schema_path}} 中 `outputs` 有值，qmd中需要添加输出
例如 


```{json}
  "outputs": [
    {
      "name": "tsv",
      "type": "file",
      "role": "TABLE"
    }
  ],
```

{% if script_type == "qmd" %}
```{r}
outputs <- list(
  tsv=mutate_genes_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
```

{% elif script_type == "R" %}

{% elif script_type == "shell" %}

{% else %}


## io_schema.json 的生成规则（{{io_schema_path}}）

`{{io_schema_path}}` 就是 io_schema.json，是「脚本参数契约」的唯一来源，它同时决定三件事：

1. `inputs[].name` = build_analysis_node_params / run_analysis_node 里 `request_param` 的键；
2. `inputs[].name` = params.json 的键，也就是脚本里 `params$<name>` 读取的参数名；
3. `inputs[]` 还会被渲染成人类用户在节点面板里填写的表单控件（由 `type` 决定控件形态）。

因此：**`request_param` 的键必须与 `inputs[].name` 逐一对应**（不能多写，多写的键会被忽略并给出 warning；
少写必填项也会给出 warning）。不要自行发明参数名，也不要省略 `name`。

### 文件结构

```{json}
{
  "inputs": [
    {
      "name": "p_threshold",
      "label": "P value threshold",
      "type": "BaseInputNumber",
      "required": true,
      "description": "差异分析的显著性阈值（表单上展示给用户）"
    }
  ],
  "outputs": [
    { "name": "tsv", "type": "file", "role": "TABLE" }
  ],
  "params": [],
  "resources": { "cpu": 4, "memory": "6GB" }
}
```

- `inputs[]`：每个条目 `name` 必填且唯一；`label`（表单展示名）、`required`（是否必填，默认 true）、
  `description`（表单说明）、`type`（控件类型，见下表）按需给出。
- `outputs[]`：每个条目 `name` 必填，`type` 通常为 `file`（也支持 `dir` / `table`），`role` 是该产物在
  `analysis_result` 中发布用的键，默认 `DEFAULT`，表格类产物写 `TABLE`（下游 FileSelect 才能按 TABLE 选到）。
- `params` 一般保持 `[]`；`resources` 是运行资源，保持默认即可。

### inputs 支持的组件类型（只使用下表）

| `type` | 表单控件 | `request_param` 值 | 脚本里怎么用 |
| --- | --- | --- | --- |
| `BaseInput` | 单行文本 | 字符串，如 `"FDR"` | `params$name` 为字符串 |
| `BaseTextArea` | 多行文本 | 字符串，可含换行 | `params$name` 为字符串 |
| `BaseTextAreaNum` | 多行文本 + 「共 N 项」提示 | 逗号分隔的字符串，如 `"CD4,CD8,FOXP3"` | `strsplit(params$name, ",")` |
| `BaseInputNumber` | 数字输入框 | JSON 数字，如 `0.05`（不要写成字符串） | `params$name` 为数值 |
| `BaseSwitch` | 开关 | JSON 布尔，如 `true` | `isTRUE(params$name)` |
| `BaseSelect` | 下拉框 | 选中的单个字符串值（多选为字符串数组） | 选项文本即取值，`params$name` 为字符串 |
| `BaseColorPicker` | 颜色选择器 | 十六进制颜色字符串，如 `"#FF0000"` | 直接用于绘图颜色 |
| `FileSelect` | 上游结果文件 + 列选择器（见下节） | **对象**：`{"file": "<file id>", "<列名>": "<列名>"}` | `params$name$path` 为文件路径 |
| `Divider` | 分区标题（纯布局） | **不参与 request_param** | 不产生参数，不要读取 |

补充约定：
- `BaseSelect` 的候选项由数据源提供（`dataKey` / `resolver.accept_formats`）。固定取值（枚举）时，
  把候选值写在条目的 `data` 里（`[{ "label": "...", "value": "..." }]` 或字符串数组），
  `request_param` 里就填这些 `value` 之一；不要填展示用的 label。
- `BaseTextAreaNum` 的值是「逗号分隔的字符串」，不是数组；`request_param` 也必须给字符串。
- `BaseInputNumber` / `BaseSwitch` 请给原生 JSON 数字 / 布尔；不要给 `"0.05"` / `"true"` 这类字符串。
- `Divider` 只用于在表单上分组，不要给它 `required`，也不要把它当参数：它不会出现在 `request_param` 中，
  脚本里也不要读取它。
- 与文件 / 上游数据相关的输入（如 `FileSelect`，见下节）不按上表处理，它有自己的一套输入输出规则。

### FileSelect：从上游结果表里选文件 + 选列（下游分析的标准输入）

`FileSelect` 用于「当前脚本要读某个上游节点产出的表格文件，并指定读其中哪几列」。它是下游分析最常用的
输入控件，`request_param` 的形态与上面那些普通参数控件**完全不同**（它是对象，不是字符串 / 数字）。

#### 输入：io_schema 里怎么写

```{json}
{
  "name": "exp",
  "label": "Expression matrix",
  "type": "FileSelect",
  "required": true,
  "input_type": "file",
  "resolver": { "accept_formats": ["TABLE"] },
  "columns": ["sample_column", "gene_column", "value_column"],
  "modes": [0, 0, 0],
  "columns_rules": [1, 1, 1],
  "groups": [0, 0, 0]
}
```

各字段作用（`columns` / `modes` / `columns_rules` / `groups` 是**按下标一一对应**的数组）：

| 字段 | 含义 |
| --- | --- |
| `name` | 该输入在 `request_param` 里的键，也是 params.json 的键 |
| `type` | 固定 `"FileSelect"` |
| `label` | 表单上「选文件」下拉框的前缀名（渲染为 `<label> File`） |
| `input_type` | 固定 `"file"`（走文件解析分支，`resolved_inputs` 里会带上路径等文件字段） |
| `resolver.accept_formats` | 该输入接受的上游产物角色，通常 `["TABLE"]`；**上游 outputs 的 `role` 必须是 `TABLE`，这里才选得到** |
| `columns` | 需要在选中文件里挑选的列，每一项会渲染成一个「`<列名>` Columns」下拉框 |
| `modes[i]` | `1` 表示第 i 列可多选（多选值为数组），`0` 表示单选 |
| `columns_rules[i]` | `1` 表示第 i 列必填 |
| `groups[i]` | `1` 表示第 i 列额外渲染一个「`<列名>` Group Name」输入框（分组名） |

`groups` / `modes` / `columns_rules` 可以省略数组项，缺省按 `0` 处理。若只选文件、不需要指定列，
`columns` 也可以不写，此时表单只渲染「选文件」下拉框 + Open / List 按钮。

#### 输入：request_param 里怎么填

值是一个**对象**，不是字符串，也不是文件 ID：

```{json}
{
  "exp": {
    "file": "123",
    "sample_column": "SampleID",
    "gene_column": "GeneID",
    "value_column": "TPM"
  }
}
```

- `file`：上游结果文件的 **file id（字符串）**，对应 `<name> File` 下拉框的选中值；
- 每个 `columns[i]`：该列选中的列名（字符串）；`modes[i]=1` 时可给字符串数组；
- 若 `groups[i]=1`，对应 group 名由表单自动生成 `node_name`，一般**不需要**在 `request_param` 里手填。

解析后（`resolved_inputs.<name>`）会得到：文件记录的字段（`id` / `analysis_result_id` / `file_name` /
`path` / `format` / `size` / `md5` 等）+ 你填的每一列 + `form_type: "FileSelect"` + `groups`（即 `columns` 列表）。
也就是说，脚本里拿到的 `params$exp` 是一个**对象**，文件路径在 `params$exp$path`，
列名在 `params$exp$sample_column` 之类的位置，而不是在 `params$exp` 这个字符串上。

#### 脚本里怎么用（qmd / R）

```{r}
# params$exp 是一个 list：文件信息 + 每一列选中的列名
exp_path    <- params$exp$path
sample_col  <- params$exp$sample_column
gene_col    <- params$exp$gene_column
value_col   <- params$exp$value_column

exp <- read.delim(exp_path, check.names = FALSE)
```

#### 输出：必须产出 role = TABLE 的表格，下游才能接着用

`FileSelect` 依赖上游 `outputs[].role == "TABLE"`，所以本脚本若产出的文件要给下游用，`io_schema.outputs`
必须显式写 `role`：

```{json}
{
  "outputs": [
    { "name": "tsv", "type": "file", "role": "TABLE" }
  ]
}
```

- 只写 `type: "file"` 时 `role` 默认是 `DEFAULT`，下游 `FileSelect` 的 `accept_formats: ["TABLE"]` 选不到；
- 产物需是可被解析列的表格（tsv / csv / xlsx），文件路径按 `outputs[].name` 写进 `outputs.json`；
- 若下游还要「按列取值」，保持首行为列名，列名不要加引号或特殊字符。

### request_param 生成示例

以上面的 inputs 为例，对应的 `request_param`（传给 build_analysis_node_params 或 run_analysis_node）为：

```{json}
{
  "p_threshold": 0.05,
  "method": "BH",
  "features": "CD4,CD8,FOXP3",
  "use_filter": true,
  "plot_color": "#FF0000"
}
```

生成后：
- params.json 里会出现与上表同名的键（脚本按这些名字读取）；
- 若 `request_param` 出现 `inputs` 里没有的键，会被忽略并返回 warning，必须回到本文件修正名字；
- 若必填项缺失，也会返回 warning，补齐后再运行。

## 创建脚本工作空间的固定流程

当用户要求「新建一个分析脚本 / 分析节点」并基于数据文件跑出结果时，必须按以下顺序推进，
并且**一次任务内走完整条流程**：从创建脚本开始，一直到 run_analysis_node 返回 success 为止。

1) create_script → 得到 script_file_path / io_schema_path / analysis_node_id。
   - 调用时一次性给出 inputs / outputs（真实的参数契约），避免创建后再补齐。
   - **create_script 的 `inputs` 就是 io_schema.json 的 `inputs`，`outputs` 就是 io_schema.json 的 `outputs`**：
     工具会把这两个数组原样写成 io_schema.json（并补齐 label / required，outputs 的 type 默认 file），
     不会再额外生成别的结构。因此这里填的每个条目，都必须满足上文「io_schema.json 的生成规则」：
     `name` 必填且唯一、`type` 取上表中的组件类型、`label` 与 `description` 面向人类用户可读。
   - io_schema_path 是参数契约的唯一来源：后续 request_param 的键、params.json 的键、
     脚本内读取的参数名，全部以它为准，不要自行发明参数名。
2) 先确认契约：结合用户需求与 read_file_by_id 拿到的数据结构（列名 / 维度 / 分组字段），
   把 io_schema.json 的 inputs / outputs 改成真实契约。
   - inputs 的 name 就是 params.json 里的键，也是脚本里取参数用的名字；
   - 输出项按实际产物命名（通常 type 为 file）；
   - io_schema.json 也用于给人类用户渲染表单，因此 label / description 要写清楚含义；
   - 需要新增 / 调整参数时，直接编辑 io_schema_path 指向的这份 JSON，规则同上文（只使用表中的
     inputs 组件类型，Divider 只做分组、不产生参数）。
3) 再写脚本主体（写入 script_file_path）：
   - 从 params.json 读取输入，读取的键名必须与 io_schema inputs 的 name 完全一致；
   - 产物写入 params.json 中的 output_dir，命名与 io_schema outputs 对应；
   - 不要硬编码路径、样本名或分组，一律通过参数传入。
4) build_analysis_node_params：按 io_schema inputs 的 name 组织 request_param（键 = name），
   把参数保存到分析节点并生成 params.json。若返回 warnings（缺必填项、存在 io_schema
   未声明的键），先修正再继续。
5) run_analysis_node 运行并查看日志；失败时必须继续迭代：读日志定位原因 → 改脚本或改参数
   → 重新 build_analysis_node_params（参数有变化时）→ 再次 run_analysis_node，
   直到返回 success=true 才算完成本次任务。

约束：
- 任务未完成的标准只有一个：run_analysis_node 返回 success=true 且产物符合 io_schema outputs。
  不得在脚本尚未跑通时结束任务，也不得把「脚本已写好」「节点已创建」当作完成。
- 若失败原因在可用信息范围内无法确定（需求歧义、缺少必要数据/参数/环境、多种方案取舍、
  create_script 报错提示需要用户配置容器模板等），调用 ask_user 向用户提问，并在拿到答复后
  继续执行流程，不要在等待用户输入时提前收尾。
- ask_user 使用要点：questions[].id 用稳定的英文短标识（如 analysis_type、group_column）；
  选项类问题用 single_choice / multiple_choice 并给出 2-5 个 option（label + value），
  必要时设置 allow_custom=true；纯信息采集用 text 并给出 placeholder；
  能一次问清的问题合并到同一次 ask_user 调用中，避免反复打断用户。
- run_analysis_node 可选传入 request_param：传了就以它为本次运行参数（并回写节点、重新生成 params.json）；不传则读取节点上已保存的参数。两种情况下都会按最新 io_schema 重新解析，并刷新 inputs_patterns / output_patterns / resolved_inputs / request_param、清空上一轮校验错误。
- 需要「先校验参数、确认 params.json 映射」时，仍优先调用 build_analysis_node_params；只是临时换一组参数跑一次时，可直接把 request_param 传给 run_analysis_node。
- 因此禁止在未调用 build_analysis_node_params（节点上无参数）且未给 run_analysis_node 传 request_param 的情况下直接运行节点。
- 修改 io_schema.json 的 inputs / outputs 后，必须重新调用 build_analysis_node_params，再运行。
- 每次运行只改必要的地方：先用日志定位失败原因，避免整段重写脚本。
- 需要完整日志时读取 run_analysis_node 返回的 log_path。
