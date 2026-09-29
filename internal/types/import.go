package types

// ImportAssayTSVRequest is the payload of the assay TSV import: a target dataset
// plus the raw TSV text (an uploaded file or pasted content).
//
// The header row names the columns. Columns that map onto a Sample/Assay
// field configure that entity (see the column maps in the data service), e.g.
// sample_name / sample_desc / assay_desc; every other column is
// treated as a File whose FileKey is the column name and whose Path is the cell
// value — so new file columns (FASTQ_R3, BAM, ...) need no code change. All
// columns except sample_name are optional, so a table without the
// desc columns imports fine. ProjectID is filled in by the handler from the
// active project and is never sent by the client.
type ImportAssayTSVRequest struct {
	DatasetID int64  `json:"dataset_id,string" binding:"required"`
	Content   string `json:"content" binding:"required"`
	ProjectID string `json:"-"`
}

// ImportAssayTSVResult summarises one import run: how many rows were processed
// and how many records were created versus updated at each hierarchy level.
type ImportAssayTSVResult struct {
	Rows           int `json:"rows"`
	SamplesCreated int `json:"samples_created"`
	SamplesUpdated int `json:"samples_updated"`
	AssaysCreated  int `json:"assays_created"`
	AssaysUpdated  int `json:"assays_updated"`
	FilesCreated   int `json:"files_created"`
	FilesUpdated   int `json:"files_updated"`
}
