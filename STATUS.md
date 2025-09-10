# Project Status

Updated: 2025-01-09

## Built
- Edge runtime (Go) with HTML UI (htmx + Alpine.js), i18n, themes (Default, Monarch)
- Buttons/products: add/remove/edit in Designer; images via URL or /public/images
- Basket and tender; barcode auto-capture (USB HID), quantity support
- Settings page: currency, country, region, tax mode (inclusive), tax rate
- Currency formatter; TaxEngine (percent-based, inclusive/exclusive)
- Storage: file JSON (default) or SQLite (UT_STORE=sqlite). JSON→SQLite auto-migration
- Theme selector stored in DB; loads on startup
- Serve local reference images via /samples when UT_SAMPLES_DIR is set
- Dockerfile + docker-compose.edge.yml (persistent volume)
- **Designer Enhancements**: Edit existing buttons, improved button management UI

## Plugin System (COMPLETE)
- Dynamic plugin management: download, install, uninstall, delete
- Plugin state tracking in SQLite (downloaded/installed status)
- External plugin repositories (GitHub-based)
- Plugin manifest system with version, route, label metadata
- Dynamic navigation menu based on installed plugins
- Plugin content embedding within main application layout
- Real-time UI updates with Alpine.js for plugin actions
- Sample FAQ plugin with external repository
- **UI Enhancements**: Button spinners, toast notifications, bulk actions
- **Version Management**: Version display and status indicators
- **Bulk Operations**: Select and manage multiple plugins simultaneously
- **Professional UX**: Loading states, success/error feedback, smooth animations

## How to run
- Local: `make build && ./bin/edge`
- With SQLite: `UT_STORE=sqlite ./bin/edge`
- Docker: `docker compose -f docker-compose.edge.yml up --build`
- Open: `/` (till), `/designer`, `/settings`, `/plugins`

## Key env vars
- UT_STORE=sqlite | UT_DEFAULT_LOCALE | UT_CURRENCY | UT_TAX_INCLUSIVE | UT_TAX_RATE | UT_SAMPLES_DIR
- See `edge.env.example` / `edge.env.dev`

## Repos/Dirs
- universal-till/cmd/edge (runtime), cmd/store (WIP store API)
- internal/{pos,ui,httpx,common}
- web/ (templates, assets, themes)
- plugins/ (structure + example manifest)
- design/ ADRs

## Feature Roadmap (Based on Open Source POS Analysis)

### Phase 1: Core POS Features (High Priority)
- **Advanced Inventory Management**
  - Item kits & bundles with customizable attributes
  - Multi-location stock tracking and transfers
  - Low stock alerts and automated reorder points
  - Barcode generation and printing
  - Supplier management and procurement tracking
  - Receivings and incoming stock management

- **Enhanced Sales & Transactions**
  - Quotations system with PDF generation
  - Invoice management and professional invoicing
  - Multi-payment methods (cash, card, digital, split payments)
  - Transaction logging and audit trails
  - Refunds & returns processing
  - Professional receipt printing and emailing

- **Customer Relationship Management**
  - Detailed customer database with purchase history
  - Loyalty programs and points-based rewards
  - Gift card management and redemption
  - Customer messaging (SMS notifications)
  - Customer analytics and behavior tracking

- **Advanced Reporting & Analytics**
  - Sales reports (daily, weekly, monthly)
  - Inventory reports and stock valuation
  - Expense tracking and categorization
  - Employee performance reports
  - Tax reports and compliance
  - Custom report generation

- **Multi-User & Security**
  - Role-based access control with granular permissions
  - Employee management and scheduling
  - Two-factor authentication and security features
  - Audit logs and activity tracking
  - GDPR compliance and data protection

### Phase 2: Restaurant & Hardware Features (Medium Priority)
- **Restaurant-Specific Features**
  - Table management and seating layouts
  - Kitchen order workflow and status tracking
  - Dynamic menu management with categories/modifiers
  - Kitchen display for order management
  - Split bill functionality

- **Hardware Integration**
  - ESC/POS receipt printer support
  - Barcode scanner integration (USB/wireless)
  - Customer display integration
  - Cash drawer management
  - Kitchen printer support
  - Payment terminal integration

- **Advanced UI/UX**
  - Touchscreen optimization
  - Multi-language support (75+ languages)
  - Theme customization and branding
  - Mobile and tablet compatibility
  - Offline mode with cloud sync

### Phase 3: Integration & Intelligence (Future)
- **Business Integrations**
  - Accounting system integration (QuickBooks, Xero)
  - eCommerce sync (Shopify, WooCommerce)
  - Email marketing (MailChimp integration)
  - RESTful APIs for third-party integrations
  - Webhook support for real-time sync

- **Business Intelligence**
  - Dashboard analytics and KPIs
  - Trend analysis and seasonal patterns
  - Profit margin analysis
  - Customer lifetime value tracking
  - AI-driven inventory optimization

- **Advanced Features**
  - Multi-tenant architecture
  - Advanced security and compliance
  - International tax compliance
  - Mobile app development
  - Advanced plugin marketplace

## Critical Missing Features (Based on Open Source POS Issues Analysis)

### **High Priority Pain Points from Community Issues**
- **Refund Processing Issues**: Robust refund mechanism handling various scenarios without errors
- **ARM64 Architecture Support**: Compilation support for ARM64 Docker environments (Raspberry Pi, Apple Silicon)
- **Serial Number Management**: Easy serial number selection during sales for inventory tracking
- **Receipt Customization**: Support for different paper sizes (letter, thermal, custom formats)
- **Category Management**: Retain active product category in orders for streamlined navigation
- **Error Handling**: Comprehensive error handling and data management practices
- **Documentation Accuracy**: Real-time documentation updates and accurate system requirements

### **User-Requested Features from Open Issues**
- **Enhanced Refund Processing**: Multi-scenario refund handling with proper error management
- **IoT Device Integration**: Cashbox management, receipt printing, customer displays, weighing scales
- **UI Component Improvements**: TextAreaPopup for multiline input, enhanced search functionality
- **Attribute Handling**: Proper handling of date attributes and complex data types
- **System Requirements**: Accurate minimum system requirements documentation
- **Version Management**: Dynamic issue templates with accurate version information

### **Common Pain Points Across All Projects**
- **Offline Mode Reliability**: Consistent offline functionality with proper data synchronization
- **Multi-Store Management**: Centralized control over multiple locations
- **User Role Clarity**: Clear definition and implementation of user roles and permissions
- **Data Import/Export**: Easy migration and data analysis capabilities
- **Mobile Responsiveness**: Full mobile and tablet compatibility
- **Performance Optimization**: Handling high transaction volumes efficiently
- **Security Enhancements**: Data encryption, secure authentication, audit trails

### **Missing Hardware Integration Features**
- **Cashbox Management**: Automatic cash drawer control and monitoring
- **Weighing Scale Support**: Integration with digital scales for weight-based products
- **Customer Display Integration**: Secondary display for customer information
- **Kitchen Display Systems**: Order management for restaurant environments
- **Payment Terminal Integration**: Direct integration with card payment systems

## Next up (immediate)
- Themes: add OSPOS/Floreant presets; refine Monarch spacing/contrast
- Store: extract to new repo; Azure/AWS upload, signed bundles, edge installer
- Items: CRUD with images upload (not just URL); search/find item list UI
- Multi-device sync: cloud sync protocol (cloud-proto); user auth/subscriptions
- **Critical**: ARM64 support for Raspberry Pi deployment
- **Critical**: Enhanced refund processing system
- **Critical**: Receipt customization and printing improvements

## Notes
- Settings are persisted (SQLite if enabled, else data/settings.json)
- Buttons JSON auto-migrates to SQLite on first run with UT_STORE=sqlite
- Plugin system supports external GitHub repositories for plugin distribution
- Plugin state (downloaded/installed) is tracked in SQLite settings table
- Dynamic navigation menu updates automatically when plugins are installed/uninstalled
- Designer page now properly displays and allows editing of existing buttons
- Plugin management includes professional UX with loading states and feedback
- All plugin operations support bulk actions for efficient management
