# ProxyLLM Documentation

Welcome to the ProxyLLM documentation. This directory contains comprehensive guides for understanding, deploying, and developing ProxyLLM.

---

## 📚 Documentation Index

### Getting Started
- **[Main README](../README.md)** - Quick start guide and basic usage
- **[Architecture & Features](./guide.md)** - System architecture and core features overview

### Core Systems
- **[Queue & Retry Design](./QUEUE_AND_RETRY_DESIGN.md)** - Priority queue and smart retry system
- **[Complete Chain Logging](./QUEUE_AND_RETRY_LOGGING.md)** - Request lifecycle logging and observability

### Development
- **[Code Review Report](./CODE_REVIEW_REPORT.md)** - Code quality analysis and improvement suggestions
- **[Implementation Review](./IMPLEMENTATION_REVIEW.md)** - Implementation verification and validation
- **[Test Templates](./TEST_TEMPLATES.md)** - Ready-to-use test code templates
- **[Agent Guidelines](../AGENTS.md)** - Guidelines for AI-assisted development

### Internal
- **[Progress & TODOs](./llm/progress_and_todos.md)** - Development status and future roadmap

---

## 🎯 Quick Navigation

### I want to...

**Understand the system**
→ Start with [Architecture & Features](./guide.md)

**Deploy ProxyLLM**
→ See [Main README](../README.md) Quick Start section

**Understand request queuing**
→ Read [Queue & Retry Design](./QUEUE_AND_RETRY_DESIGN.md)

**Debug issues**
→ Check [Complete Chain Logging](./QUEUE_AND_RETRY_LOGGING.md)

**Contribute code**
→ Review [Code Review Report](./CODE_REVIEW_REPORT.md) and [Agent Guidelines](../AGENTS.md)

**Write tests**
→ Use [Test Templates](./TEST_TEMPLATES.md)

---

## 📖 Document Summaries

### Architecture & Features (guide.md)
Comprehensive overview of ProxyLLM's architecture, including:
- System components and data flow
- Priority queue system
- Smart retry and failover
- Weighted load balancing
- Rate limiting (RPM/TPM)
- Logging and observability
- Configuration options

### Queue & Retry Design
Detailed design document for the request queuing and retry system:
- Priority-based queue implementation
- Worker pool architecture
- Retry logic and error classification
- Timeout control
- Configuration options

### Complete Chain Logging
Logging system design and usage:
- Log event types and structure
- JSON log format specification
- Query examples
- Error attribution
- Monitoring integration

### Code Review Report
Analysis of code quality:
- Test coverage analysis
- Identified issues (critical, medium, minor)
- Improvement suggestions
- Testing recommendations

### Implementation Review
Verification of implemented features:
- Feature completeness check
- Code quality assessment
- Performance evaluation
- Security review
- Final approval status

### Test Templates
Ready-to-use test code:
- Unit test templates
- Integration test examples
- Test coverage guidelines
- Best practices

---

## 🔄 Document Status

| Document | Status | Last Updated |
|----------|--------|--------------|
| guide.md | ✅ Complete | 2026-03-11 |
| QUEUE_AND_RETRY_DESIGN.md | ✅ Complete | 2026-03-11 |
| QUEUE_AND_RETRY_LOGGING.md | ✅ Complete | 2026-03-11 |
| CODE_REVIEW_REPORT.md | ✅ Complete | 2026-03-11 |
| IMPLEMENTATION_REVIEW.md | ✅ Complete | 2026-03-11 |
| TEST_TEMPLATES.md | ✅ Complete | 2026-03-11 |
| progress_and_todos.md | ✅ Complete | 2026-03-11 |

---

## 🤝 Contributing

When contributing to ProxyLLM:

1. Read [Agent Guidelines](../AGENTS.md) for development standards
2. Check [Progress & TODOs](./llm/progress_and_todos.md) for current status
3. Review [Code Review Report](./CODE_REVIEW_REPORT.md) for known issues
4. Use [Test Templates](./TEST_TEMPLATES.md) when writing tests
5. Follow the architecture described in [guide.md](./guide.md)

---

## 📞 Support

For questions or issues:
- Check the documentation first
- Review [Complete Chain Logging](./QUEUE_AND_RETRY_LOGGING.md) for debugging
- Open an issue on GitHub with relevant logs

---

**Documentation Version**: 1.0  
**Last Updated**: 2026-03-11
