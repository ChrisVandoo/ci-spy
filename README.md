## ci-spy

A tool that watches CI and reports when and (probably) why it is exploding.

### POC

Project Goal: notify me when master or a release branch starts exploding and give me a hypothesis as to why this is happening that includes all the information I need to validate the hypothesis and/or tag the person responsible.

- ideally would trigger on webhook, could initially be configured to poll Github hourly? maybe every 20 or 30 minutes
    - CI Conductor is very helpful for quickly getting the state of CI, should consider if it is more effective to query this instead
- I should be notified, could this just be a slack ping? or configure helpful terminal notifications?
    - this should be a configurable schedule—I don’t want notifications after hours or on weekends, I also don’t want to abuse the Github API (or consume all my AI usage)
- needs to store failures to deduplicate when one failures causes a train of failing commits
    - do quick & cheap analysis to see if this is the same issue before triggering the more expensive bit
- I want a proposed analysis, root cause, and potential fix
    - what change caused the failure
    - who made the change, (name & slack handle)
    - time since initial failure
    - specific change that likely caused the failure
    - proposed solution

Also want this to be something that could be integrated into CI Conductor eventually, shouldn’t be tied to my local LLM harness. Might be a good place to use my Anthropic credits though.
