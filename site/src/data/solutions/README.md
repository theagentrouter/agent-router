# Solutions Data

This directory contains the data for the "Solutions built on Agent Router"
section of the homepage: commercial products, managed services, and
distributions that are built on top of Agent Router.

This is separate from [adopters](../adopters/README.md). Adopters are
organizations that _run_ Agent Router; solutions are products that _package_
it for others.

## Adding Your Solution

### Quick Add (Recommended)

**[Edit solutions.json on GitHub →](https://github.com/theagentrouter/agent-router/edit/main/site/src/data/solutions/solutions.json)**

This will open the GitHub editor in your browser where you can:

1. Add your solution's entry at the end of the JSON array
2. Commit your changes
3. GitHub will automatically create a pull request for you!

No need to clone the repository or set up a development environment.

### JSON Format

Add your solution at the end of the array in `solutions/solutions.json`:

```json
{
  "name": "Your Product Name",
  "vendor": "Your Company",
  "deployment": "Hosted",
  "logoUrl": "https://yoursite.com/logo.svg",
  "url": "https://yourcompany.com/product",
  "description": "One or two sentences on what your product adds on top of Agent Router."
}
```

### Fields

- **`name`** (required): The product name shown as the card title
- **`vendor`** (required): The company or project offering the solution
- **`description`** (required): One or two sentences shown on the card. Say what
  the product adds on top of Agent Router, not what Agent Router does.
- **`url`** (required): The product page the card links to
- **`logoUrl`** (optional): Logo of the vendor or product, either:
  - External URL: `https://yoursite.com/logo.svg` (easiest!)
  - Local path: `/img/solutions/your-logo.svg` (requires uploading the logo file
    to `site/static/img/solutions/`)
- **`deployment`** (optional): A short badge such as `Hosted`, `Self-managed`,
  or `Managed service`

### Logo Specifications

- **Format**: SVG preferred (PNG also acceptable)
- **Shape**: Horizontal wordmark works best; logos are shown at 28px height
- **Background**: Transparent

### Guidelines

- Solutions should be built on, bundle, or extend Agent Router. General AI
  products that merely integrate with it belong in the adopters list instead.
- Keep the description factual and short. Marketing superlatives will be
  trimmed in review.
- Please ensure you have permission to use the logo.
- We reserve the right to remove entries that don't meet our community standards.

### Display Order

Solutions are displayed in the order they appear in the file. Add your entry
at the end of the array.

### Need Help?

If you have questions about adding your solution:

- Ask in [GitHub Discussions](https://github.com/theagentrouter/agent-router/discussions)
- Join our [Discord community](https://discord.gg/xuxtPq43gZ)
