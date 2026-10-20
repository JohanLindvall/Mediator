# Getting help

Start with the [quick start](README.md#quick-start),
[documentation index](docs/README.md) or
[troubleshooting guide](docs/troubleshooting.md).

| You need to… | Go to… |
| --- | --- |
| Ask about setup, playback or a receiver | [Ask a question](https://github.com/JohanLindvall/Mediator/issues/new?template=question.yml) |
| Report something broken | [Report a bug](https://github.com/JohanLindvall/Mediator/issues/new?template=bug_report.yml) |
| Suggest a change | [Request a feature](https://github.com/JohanLindvall/Mediator/issues/new?template=feature_request.yml) |
| Report a vulnerability | [Private security reporting](https://github.com/JohanLindvall/Mediator/security/advisories/new) |
| Help fix or improve the project | [Contributing](CONTRIBUTING.md) |

Search [existing issues](https://github.com/JohanLindvall/Mediator/issues)
first; a matching report may already have a workaround. Add relevant details
to an existing issue instead of opening a duplicate.

Include the version from Preferences or `/api/info`, installation method
(Docker, Compose or native), server OS and architecture, browser/device, and
steps to reproduce. For playback, describe the container and codecs, such as
"MKV with HEVC video and two soundtracks".

For Compose logs, use `docker compose logs --tail=100 mediator`. If more detail
is needed, add `-debug` before the directory arguments, reproduce once, then
remove it. **Review logs before posting:** replace real media names, personal
paths and hostnames with invented equivalents, and remove signed media URLs,
tokens and credentials. Do not paste the complete `/api/info` response: it
can include a media token. Do not attach library screenshots or private media.

This is a community project without a guaranteed response time. A short,
reproducible report with the relevant redacted log helps others investigate.
