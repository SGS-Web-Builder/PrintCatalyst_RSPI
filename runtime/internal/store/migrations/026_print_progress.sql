-- OS progress is separate from durable submission and payment state.
ALTER TABLE print_submissions ADD COLUMN progress TEXT NOT NULL DEFAULT '';
ALTER TABLE print_submissions ADD COLUMN progress_detail TEXT NOT NULL DEFAULT '';
