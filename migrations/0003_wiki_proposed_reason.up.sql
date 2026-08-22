-- S35: persist the proposal's reason on the page row. The S21 propose_wiki_page tool
-- accepted a `reason` ("You corrected me twice about migrations") but only recorded it in
-- the activity payload and the wiki.proposed event — the review UI needs it on the page
-- itself (the proposal banner shows why the agent proposed), so it joins the other
-- provenance columns (proposed_by_run_id, proposed_base_version, proposal_target_id).
ALTER TABLE wiki_pages ADD COLUMN proposed_reason TEXT;
