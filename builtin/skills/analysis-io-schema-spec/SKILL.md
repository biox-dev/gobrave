---
name: analysis-io-schema-spec
description: 生成 / 修改脚本 io_schema.json（get_script 返回的 io_schema_path）的输入契约规范：只允许 BaseInput、BaseTextArea、BaseTextAreaNum、BaseInputNumber、BaseSwitch、BaseColorPicker、ThreeColorPicker、Divider、BaseSelect、FileSelect 十种类型；逐项说明字段写法、request_param 取值与后端解析后的 params.json 结构；其中 FileSelect 给出完整字段表、UI 渲染与 request_param 形态、后端合并文件记录的解析规则、脚本读取方式与常见错误。编写、校验 io_schema.json 或推算 params.json 时使用。
user-invocable: true
disable-render: true
---

## 脚本 io_schema.json 输入契约规范

`get_script` 返回的 `io_schema_path` 就是脚本目录下的 `io_schema.json`。它是一份**唯一**的契约，
同时被三处消费，因此写错一处就会三处全错：

1. **人类表单**：`inputs[]` 被渲染成用户在分析节点面板里填写的控件（由 `type` 决定控件形态）；
2. **`request_param`**：`build_analysis_node_params` / `create_script_analysis` 的入参键名，
   必须与 `inputs[].name` 一一对应；
3. **`params.json`**：后端 `analysisform.BuildParseAnalysisResult` 把 `request_param` 按 `inputs[]`
   解析后写出 `params.json`，脚本从这里读参数。

~~~text
用户填表单  →  request_param（键 = inputs[].name）
            →  BuildParseAnalysisResult（按 inputs[].type / db / input_type 解析）
            →  params.json（键 = inputs[].name，值已按下面的规则变形）
            →  脚本读 params
~~~

**只改 `io_schema_path` 指向的 JSON 即可**，不需要重建 script 或分析节点：`build_analysis_node_params`
与 `run_analysis_node` 每次执行都会重新读取最新的 `io_schema.json`。

> 本技能只覆盖 `inputs`（表单输入）。`outputs` / `params` / `resources` / `ui` / `scatter` / `workflow`
> 等其余顶层键沿用 `analysis-script-spec`、`analysis-script-workflow` 两个技能的约定，不要在这里发明新结构。

---

## 1. 文件骨架

~~~json
{
  "inputs": [
    {
      "name": "top_num",
      "label": "Top Number",
      "type": "BaseInputNumber",
      "required": true,
      "initialValue": 0
    }
  ],
  "outputs": [
    { "name": "deg", "type": "file", "role": "TABLE" }
  ],
  "params": [],
  "resources": { "cpu": 4, "memory": "6GB" }
}
~~~

- `inputs[]`：表单条目，`name` 必填且唯一（`Divider` 除外）。
- `outputs[]`：产物声明（`name` / `type` / `role`），产物要能被下游 `FileSelect` 选到必须写
  `"role": "TABLE"`。
- `params` 保持 `[]`；`resources` 保持默认即可。

---

## 2. inputs 条目的通用字段

| 字段 | 必填 | 作用 |
| --- | --- | --- |
| `name` | 是 | 参数键：`request_param` 的键、`params.json` 的键、脚本里读取的名字。**`Divider` 不写 name**。 |
| `type` | 是 | 控件类型，只能是第 3 节表格里的 10 种之一。 |
| `label` | 否 | 表单上展示给用户的名称；缺失时用 `name`。面向人类写清楚。 |
| `required` | 否 | 顶层布尔值。**后端用它做缺失校验**：`required: true` 而 `request_param` 里没有这个键时，`build_analysis_node_params` 会返回 warning。 |
| `rules` | 否 | antd 表单校验数组，控制**人类表单**的必填提示：`[{"required": true, "message": "This field cannot be empty!"}]`。后端不读它。 |
| `tooltip` | 否 | 表单问号提示。 |
| `initialValue` | 否 | 表单默认值。类型要与控件匹配：数字控件给数字、开关给布尔、`ThreeColorPicker` 给 3 色数组。 |
| `extra` | 否 | 控件下方的辅助说明文字。 |
| `col` | 否 | 表单栅格宽度，1–24，缺省 24（独占一行）。 |
| `depends` | 否 | 条件显示（依赖其它字段取值），不确定就省略。 |
| `db` | 否 | **只有 `FileSelect` 需要且必须为 `true`**（见第 4.5 节）。其余类型不要写。 |
| `input_type` | 否 | **只有 `FileSelect` 需要且必须为 `"file"`**。 |
| `resolver` | 否 | **只有 `FileSelect` 需要**：`{"accept_formats": ["TABLE"]}`，决定可选的上游文件。 |
| `data` | 否 | `BaseSelect` 的内联候选值（`[{"label": "...", "value": "..."}]`）。 |

**必填要写两处**：顶层 `required: true`（后端告警）+ `rules`（人类表单必填标记）。只写 `rules`
后端不会提示缺失；只写 `required` 人类表单不会拦截空值。

---

## 3. 支持的 10 种控件类型

只允许下表这些 `type`，不要使用其它控件类型（其余类型尚未验证，渲染 / 解析行为不保证）。

| `type` | 表单控件 | `request_param` 值 | `params.json` 值 | 脚本读取 |
| --- | --- | --- | --- | --- |
| `BaseInput` | 单行文本 | 字符串 `"FDR"` | 原样字符串 | `params$name` |
| `BaseTextArea` | 多行文本 | 字符串，可含换行 | 原样字符串 | `params$name` |
| `BaseTextAreaNum` | 多行文本 + 「共 N 项」 | **逗号分隔的字符串** `"CD4,CD8"`（不是数组） | 原样字符串 | `strsplit(params$name, ",")[[1]]` |
| `BaseInputNumber` | 数字输入框 | JSON 数字 `0.05`（不要写 `"0.05"`） | 数字 | `params$name` |
| `BaseSwitch` | 开关 | JSON 布尔 `true` | 布尔 | `isTRUE(params$name)` |
| `BaseColorPicker` | 单色选择器 | 十六进制字符串 `"#FF0000"` | 原样字符串 | `params$name` |
| `ThreeColorPicker` | 低 → 高 三色 | **对象** `{"color1": "#..", "color2": "#..", "color3": "#.."}` | 同左（对象） | `params$name$color1` |
| `Divider` | 分区标题（纯布局） | **不参与**（不产生参数） | 无此键 | 不要读取 |
| `BaseSelect` | 下拉框 | 选中项的 `value`（字符串 / 数字） | 原样值 | `params$name` |
| `FileSelect` | 上游结果表 + 列选择 | **对象**（见第 4 节） | **文件记录 + 选择项的合并对象** | `params$name$path` 等 |

补充说明：

- `BaseInputNumber` / `BaseSwitch` 的 `request_param` 与 `initialValue` 都要给原生 JSON 数字 / 布尔。
- `ThreeColorPicker` 的 `initialValue` 是三元数组 `["#aaa", "#bbb", "#ccc"]`，但**提交值是对象**
  （控件内部把三个颜色分别绑在 `color1` / `color2` / `color3` 上），脚本里按 `$color1` 取。
- `Divider` 只用于分组，不要写 `name` / `required`，脚本里也不要读它；它不会出现在 `request_param`
  与 `params.json` 中。
- `BaseSelect` 的候选值来源：
  - 固定枚举（推荐）：把候选直接写在条目 `data` 里，`request_param` 填其中的 `value`
    （不要填展示用的 `label`）；
  - 项目级数据源：`dataKey`（如 `group_field`）由前端 `dataMap` 解析，渲染器支持但编辑器不暴露该字段，
    只在确有项目级候选列表时使用；
  - 需要多选时控件本身支持 `"mode": "multiple"`（值变成数组），同样属于编辑器之外的字段，非必要不要用。

---

## 4. FileSelect（下游分析的标准输入，最重要）

`FileSelect` 表示「本脚本要读某个上游节点产出的表格文件，并指定读其中哪几列」。它的
`request_param` 值是一个**对象**，`params.json` 里也是**对象**（不是路径字符串），这是它与普通
参数控件最大的区别。

### 4.1 io_schema 里怎么写（完整示例）

~~~json
{
  "name": "count",
  "label": "count",
  "type": "FileSelect",
  "input_type": "file",
  "db": true,
  "resolver": { "accept_formats": ["TABLE"] },
  "columns": ["feature", "control", "treatment"],
  "modes": [0, 1, 1],
  "columns_rules": [1, 1, 1],
  "groups": [0, 0, 0],
  "required": true,
  "rules": [{ "required": true, "message": "This field cannot be empty!" }]
}
~~~

### 4.2 各字段作用

| 字段 | 作用 |
| --- | --- |
| `name` | 参数键：`request_param.<name>`、`params.json` 里的 `<name>`、脚本里的 `params$<name>`。 |
| `label` | 文件下拉框的前缀名，渲染成 `<label> File`。 |
| `type` | 固定 `"FileSelect"`。 |
| `input_type` | 固定 `"file"`。缺了它后端不会走文件解析分支。 |
| `db` | **必须 `true`**。只有 `db: true` 的条目才会被 `BuildParseAnalysisResult` 按数据库文件记录解析；缺省的条目只把原始 `request_param` 值原样透传，脚本里拿不到 `path`。 |
| `resolver.accept_formats` | 该输入接受的上游产物角色。前端用它去 `dataMap` 取候选文件（`dataMap[role]`，多个角色是「任选其一」并合并去重），后端用它给候选分组。**上游 `outputs[].role` 必须是这里列出的值（通常 `"TABLE"`）**，否则下拉框选不到那个文件。不要省略：省略后该条目不会贡献任何候选项。 |
| `columns` | **本脚本自定义的列参数别名**（不是文件里的真实列名）。每一项会在表单上渲染一个 `<别名> Columns` 下拉框，用户从**选中文件的真实列**里挑一列，`request_param` 里的键就是这里的别名。 |
| `modes[i]` | 第 i 列是否多选：`1` = 多选（值为字符串数组），`0` = 单选（值为字符串）。缺省按 `0`。 |
| `columns_rules[i]` | 第 i 列是否必填：`1` = 必填。缺省按 `0`。 |
| `groups[i]` | `1` = 额外渲染 `<别名> Group Name` 输入框，用户填一个分组名（分组名会被前端拼进 `node_name`）。缺省按 `0`。 |
| `append` | 可选。给这条输入追加子控件，子项 `type` 只能是 `BaseInput` / `BaseInputNumber` / `BaseTextAreaNum` / `BaseSelect`；它们的值会挂在**同一个对象**下（`request_param.<name>.<子项 name>`）。 |
| `rules` | antd 文件选择框的整体校验（列必填由 `columns_rules` 控制，不是 `rules`）。 |
| `filter` / `group` / `groupField` | 数据源联动（筛选候选文件 / 按元数据字段分组）。`filter` 里是函数，JSON 无法表达，**生成 io_schema 时不要写这些字段**。 |

`columns` / `modes` / `columns_rules` / `groups` 是**按下标一一对应**的并行数组。

### 4.3 前端如何渲染 / 收集（决定 request_param 长什么样）

`go-brave-ui/src/components/form-components/components/collected/collected-assay-select.tsx`
里的 `FileSelect` 会给每个字段一个 antd `Form.Item` name：

| 表单控件 | `request_param.<name>` 下的键 |
| --- | --- |
| 文件下拉框（候选来自 `dataMap[role]`，`value` 是文件的数据库主键 id） | `file` |
| Excel 表格的 sheet 下拉框（仅 xlsx/xls 出现，前端会自动填第一个 sheet） | `sheet` |
| 每个 `columns[i]` 的列下拉框（候选是该文件的真实列名） | `columns[i]` 这个别名 |
| `groups[i] == 1` 时的分组名输入框 | `<别名>_group` |
| 隐藏的 `node_name` 输入框（由所有 `<别名>_group` 用 `_vs_` 拼接自动生成） | `node_name` |
| `append[]` 子控件 | 子项自己的 `name` |

所以 `request_param` 是这样一个对象（多选列 `modes[i] = 1`）：

~~~json
{
  "count": {
    "file": "123",
    "feature": "gene_id",
    "control": ["S1", "S2"],
    "treatment": ["S3", "S4"]
  }
}
~~~

- `file` 必须填**上游文件的主键 id**（字符串即可），不能用文件名或路径；
- 列的值填**文件里的真实列名**（用户在下拉框里选到的那个），不是 io_schema 里的别名；
- `modes[i] = 1` 的列给数组，`0` 给单个字符串。

### 4.4 后端如何解析（`gobrave/internal/analysisform/parse_result_builder.go`）

只有同时满足 `db: true` + `input_type: "file"` + `type: "FileSelect"` 的条目才会走这段逻辑
（`formType == FileSelect` 分支）：

1. **取文件 id**：`extractIDs` 优先读对象里的 `file` 键。**没有 `file` 就等于没有选择**，
   该条目不会被解析，`params.json` 里只会保留原始对象（没有 `path`）。
2. **查文件记录**：按 id 查 `go_file`，查不到直接报错
   `data inconsistency: file id <id> not found`（会让 `build_analysis_node_params` 失败）。
3. **摊平文件记录**：`buildCompatAnalysisResultItem` 生成
   `id` / `analysis_result_id` / `assay_id` / `file_name` / `component_id` / `file_type` /
   `path` / `format` / `size` / `md5` / `storage` / `description`。
4. **合并请求值**：把 `request_param.<name>` 对象的每个键**覆盖式**写进这个记录
   （所以 `file` / `sheet` / 各列别名 / `<别名>_group` / `node_name` / append 子字段都会出现在里面；
   若请求里给了与文件记录同名的键，会覆盖数据库值）。
5. **追加两个固定键**：`form_type: "FileSelect"` 与 `groups`（= `columns` 别名数组）。

解析结果就是这个条目的 `params.json` 值。

### 4.5 解析后的 params.json（可直接推算）

以上面的 `count` 为例，`params.json` 中会出现：

~~~json
{
  "count": {
    "id": "123",
    "analysis_result_id": "123",
    "assay_id": "",
    "file_name": "count.tsv",
    "component_id": "",
    "file_type": "tsv",
    "path": "/data2/brave_analysis_workspace/data/36/.../count.tsv",
    "format": "tsv",
    "size": 20480,
    "md5": "…",
    "storage": "…",
    "description": "",
    "file": "123",
    "feature": "gene_id",
    "control": ["S1", "S2"],
    "treatment": ["S3", "S4"],
    "form_type": "FileSelect",
    "groups": ["feature", "control", "treatment"]
  },
  "output_dir": "/…/output",
  "project_dir": "/…/data/36",
  "node_cached_dir": "/…/cached"
}
~~~

要点：

- **文件路径在 `params$<name>$path`**，不是 `params$<name>`；
- 列名在 `params$<name>$<别名>`（别名 = io_schema 的 `columns[i]`）；
- 除 `inputs[].name` 之外，`params.json` 只有 `output_dir` / `project_dir` / `node_cached_dir`
  这几个运行期公共键，其它顶层键（如 `analysis_name`）不会传给脚本，不要依赖。

### 4.6 脚本里怎么读

R / qmd：

~~~r
params <- jsonlite::read_json("params.json", simplifyVector = TRUE)

count_df <- read_tsv(params$count$path, show_col_types = FALSE)
feature_col <- params$count$feature                 # 真实列名
samples <- c(params$count$control, params$count$treatment)
expr <- count_df[, samples]
~~~

Python：

~~~python
import json, sys
params = json.load(open(sys.argv[1]))

df = pd.read_csv(params["count"]["path"], sep="\t")
feature_col = params["count"]["feature"]
samples = params["count"]["control"] + params["count"]["treatment"]
~~~

### 4.7 FileSelect 常见错误

- **漏写 `db: true`** → 后端不解析，脚本取不到 `path`（拿到的是原始对象）。
- **漏写 `input_type: "file"`** → 同上，不会走文件解析分支。
- **漏写 `resolver.accept_formats`** → 候选列表为空，表单里选不到任何文件。
- **上游 `outputs[].role` 不是 `TABLE`**（默认是 `DEFAULT`）→ `accept_formats: ["TABLE"]` 选不到它。
- **`file` 填了文件名 / 路径 / 表格标题** → 解析报错或取不到记录；必须是文件主键 id。
- **`modes` / `columns_rules` / `groups` 长度与 `columns` 不一致** → 缺失项按 `0`，容易造成「以为必填却没校验」。
- **把真实列名写进 `columns`** → `columns` 是**参数别名**（脚本里用 `params$name$<别名>`）；
  真实列名由用户在表单里选，写死后反而让参数名与文件列绑定。
- **没有 `file` 键** → 该输入静默不解析，`params.json` 里没有这条的文件信息。

---

## 5. 生成 io_schema.json 的检查清单

1. 每个 `inputs[]` 项：`name` 唯一且非空；`type` 属于第 3 节的 10 种。
2. `Divider` 不写 `name`，不作为参数。
3. 数值 / 布尔控件的 `initialValue` 与 `request_param` 不写成字符串。
4. 必填项：顶层 `required: true`（后端告警）+ `rules`（人类表单）。
5. `FileSelect`：`input_type: "file"`、`db: true`、`resolver.accept_formats` 三件套齐全；
   `columns` 用参数别名；`modes` / `columns_rules` / `groups` 与 `columns` 对齐。
6. 需要给下游复用的产物：`outputs[]` 写 `"role": "TABLE"`，且产物是 tsv / csv / xlsx。
7. 不需要任何来源不明的控件类型；不确定时回到本文档第 3 节挑一个最接近的。
