package tasks

import "sub_scribe/internal/jobs"

// Register wires the task-type handlers into the job registry. TaskGenerateFeed
// is intentionally absent: feeds are regenerated inside DownloadMedia, so a
// standalone feed handler would double the work.
func Register(reg *jobs.Registry, deps Deps) {
	reg.Register(jobs.TaskIndexSource, IndexHandler(deps.Indexer))
	reg.Register(jobs.TaskDownloadMedia, DownloadHandler(deps.Downloader))
	reg.Register(jobs.TaskCleanup, CleanupHandler(deps.Retainer))
	reg.Register(jobs.TaskRedownload, RedownloadHandler(deps.Redownloader))
	reg.Register(jobs.TaskPruneJobs, PruneJobsHandler(deps.JobPruner))
	reg.Register(jobs.TaskRenameFiles, RenameHandler(deps.Renamer))
}
