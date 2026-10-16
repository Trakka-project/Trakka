// Package shoptemplates holds Trakka's built-in list templates — ready-made
// lists a new list can start from (POST /api/v1/lists with template_key):
//
//   - purchase templates (cleaning, travel, moving, back to school and home
//     maintenance) for a "Courses" list, each with its essential items,
//     their store aisle and an estimated unit price the user can then
//     change;
//   - task templates (cleaning chores, moving to-do, back-to-school
//     paperwork) for a "Tâches" list: tasks with no price or aisle, the
//     chores repeating on a recurrence rule, the rest a one-off checklist.
//
// They live in code rather than in the database so they are translated (the
// frontend's languages, internal/validate.SupportedLanguages) and a new
// version ships with a release, without a migration. A House's own
// templates, saved from one of its lists, are stored in shopping_templates
// instead (internal/db); both reach the client through GET
// /api/v1/shopping-templates in the same shape (models.ShoppingTemplate).
//
// Stdlib-only apart from internal/models, internal/budget's rounding and
// internal/catalog's aisle keys, like internal/catalog itself.
package shoptemplates

import (
	"trakka/internal/budget"
	"trakka/internal/catalog"
	"trakka/internal/models"
)

// Built-in template keys: purchase templates first, then task templates.
const (
	KeyCleaning        = "cleaning"
	KeyTravel          = "travel"
	KeyMoving          = "moving"
	KeyBackToSchool    = "back_to_school"
	KeyHomeMaintenance = "home_maintenance"

	KeyCleaningTasks     = "cleaning_tasks"
	KeyMovingTasks       = "moving_tasks"
	KeyBackToSchoolTasks = "back_to_school_tasks"
)

// fallbackLanguage is used for a language a template has no text for.
const fallbackLanguage = "fr"

// text is one string in every supported language.
type text struct{ fr, en string }

func (t text) in(lang string) string {
	if lang == "en" {
		return t.en
	}
	return t.fr
}

type item struct {
	title    text
	quantity int
	price    float64
	aisle    string
}

// task is one item of a task template: a one-off task when rule is "",
// else a chore repeating on that canonical rule (internal/recurrence).
type task struct {
	title text
	rule  string
}

// A template has either items (a purchase template) or tasks (a task
// template).
type template struct {
	key   string
	icon  string
	name  text
	items []item
	tasks []task
}

// The list types the built-in templates create: a "Courses" list for a
// purchase template — the one that shows store aisles ("Par rayon") next to
// prices — and a "Tâches" list for a task template, the one with
// recurrence and due dates.
const (
	purchaseListType = "groceries"
	taskListType     = "todo"
)

// Recurrence rules of the task templates, in canonical spelling
// (internal/validate.Recurrence returns them unchanged).
const (
	weekly           = "FREQ=WEEKLY"
	everyTwoWeeks    = "FREQ=WEEKLY;INTERVAL=2"
	monthly          = "FREQ=MONTHLY"
	everyThreeMonths = "FREQ=MONTHLY;INTERVAL=3"
	everySixMonths   = "FREQ=MONTHLY;INTERVAL=6"
	yearly           = "FREQ=YEARLY"
)

// builtins are the templates, in the order the picker shows them. Prices
// are rough French retail prices in euros (2026), per unit or per pack as
// the title says — a starting estimate, meant to be edited.
var builtins = []template{
	{
		key:  KeyCleaning,
		icon: "🧹",
		name: text{"Ménage & Entretien", "Cleaning & Upkeep"},
		items: []item{
			{text{"Liquide vaisselle", "Dish soap"}, 1, 2.50, catalog.AisleHousehold},
			{text{"Pastilles lave-vaisselle", "Dishwasher tablets"}, 1, 8.90, catalog.AisleHousehold},
			{text{"Lessive", "Laundry detergent"}, 1, 11.90, catalog.AisleHousehold},
			{text{"Adoucissant", "Fabric softener"}, 1, 4.50, catalog.AisleHousehold},
			{text{"Nettoyant multi-surfaces", "All-purpose cleaner"}, 1, 3.20, catalog.AisleHousehold},
			{text{"Nettoyant WC", "Toilet cleaner"}, 1, 2.90, catalog.AisleHousehold},
			{text{"Vinaigre ménager", "Cleaning vinegar"}, 1, 1.80, catalog.AisleHousehold},
			{text{"Éponges", "Sponges"}, 1, 2.40, catalog.AisleHousehold},
			{text{"Chiffons microfibre", "Microfiber cloths"}, 1, 5.90, catalog.AisleHousehold},
			{text{"Gants de ménage", "Rubber gloves"}, 1, 2.20, catalog.AisleHousehold},
			{text{"Sacs poubelle", "Bin bags"}, 1, 4.20, catalog.AisleHousehold},
			{text{"Essuie-tout", "Paper towels"}, 1, 3.60, catalog.AisleHousehold},
			{text{"Papier toilette", "Toilet paper"}, 1, 5.50, catalog.AisleHygiene},
			{text{"Savon pour les mains", "Hand soap"}, 1, 2.10, catalog.AisleHygiene},
		},
	},
	{
		key:  KeyTravel,
		icon: "✈️",
		name: text{"Vacances / Voyage", "Holidays / Travel"},
		items: []item{
			{text{"Crème solaire", "Sunscreen"}, 1, 12.90, catalog.AisleHygiene},
			{text{"Après-soleil", "After-sun lotion"}, 1, 7.50, catalog.AisleHygiene},
			{text{"Produits de toilette format voyage", "Travel-size toiletries"}, 1, 9.90, catalog.AisleHygiene},
			{text{"Trousse de premiers secours", "First-aid kit"}, 1, 12.00, catalog.AisleHygiene},
			{text{"Anti-moustiques", "Insect repellent"}, 1, 8.50, catalog.AisleHygiene},
			{text{"Adaptateur de prise universel", "Universal plug adapter"}, 1, 14.90, catalog.AisleElectronics},
			{text{"Batterie externe", "Power bank"}, 1, 24.90, catalog.AisleElectronics},
			{text{"Cadenas de valise", "Luggage lock"}, 1, 8.90, catalog.AisleClothing},
			{text{"Coussin de voyage", "Travel pillow"}, 1, 15.90, catalog.AisleClothing},
			{text{"Lunettes de soleil", "Sunglasses"}, 1, 19.90, catalog.AisleClothing},
			{text{"Maillot de bain", "Swimsuit"}, 1, 25.00, catalog.AisleClothing},
			{text{"Serviette de plage", "Beach towel"}, 1, 14.90, catalog.AisleClothing},
			{text{"Bouteilles d'eau", "Bottled water"}, 6, 0.50, catalog.AisleDrinks},
			{text{"Encas pour le trajet", "Snacks for the journey"}, 1, 8.00, catalog.AisleBreakfast},
		},
	},
	{
		key:  KeyMoving,
		icon: "📦",
		name: text{"Déménagement", "Moving House"},
		items: []item{
			{text{"Cartons de déménagement", "Moving boxes"}, 20, 2.50, catalog.AisleDIY},
			{text{"Ruban adhésif d'emballage", "Packing tape"}, 4, 3.50, catalog.AisleDIY},
			{text{"Dévidoir de ruban adhésif", "Tape dispenser"}, 1, 9.90, catalog.AisleDIY},
			{text{"Papier bulle", "Bubble wrap"}, 2, 12.90, catalog.AisleDIY},
			{text{"Couvertures de déménagement", "Moving blankets"}, 2, 11.90, catalog.AisleDIY},
			{text{"Cutter", "Box cutter"}, 1, 4.50, catalog.AisleDIY},
			{text{"Ampoules", "Light bulbs"}, 4, 3.90, catalog.AisleDIY},
			{text{"Feutres marqueurs", "Permanent markers"}, 1, 4.90, catalog.AisleStationery},
			{text{"Étiquettes", "Labels"}, 1, 3.90, catalog.AisleStationery},
			{text{"Housse de protection pour matelas", "Mattress bag"}, 1, 9.90, catalog.AisleHousehold},
			{text{"Sacs poubelle grande contenance", "Heavy-duty bin bags"}, 1, 6.50, catalog.AisleHousehold},
			{text{"Produits de nettoyage", "Cleaning supplies"}, 1, 15.00, catalog.AisleHousehold},
		},
	},
	{
		key:  KeyBackToSchool,
		icon: "🎒",
		name: text{"Rentrée scolaire", "Back to School"},
		items: []item{
			{text{"Cartable", "School bag"}, 1, 39.90, catalog.AisleStationery},
			{text{"Trousse", "Pencil case"}, 1, 7.90, catalog.AisleStationery},
			{text{"Cahiers grand format", "Large notebooks"}, 6, 1.50, catalog.AisleStationery},
			{text{"Classeur", "Ring binder"}, 2, 3.50, catalog.AisleStationery},
			{text{"Copies doubles", "Loose-leaf paper"}, 1, 3.20, catalog.AisleStationery},
			{text{"Stylos bille", "Ballpoint pens"}, 1, 3.90, catalog.AisleStationery},
			{text{"Crayons à papier", "Pencils"}, 1, 2.50, catalog.AisleStationery},
			{text{"Gomme et taille-crayon", "Eraser and sharpener"}, 1, 2.90, catalog.AisleStationery},
			{text{"Crayons de couleur", "Coloured pencils"}, 1, 5.90, catalog.AisleStationery},
			{text{"Règle et équerre", "Ruler and set square"}, 1, 3.50, catalog.AisleStationery},
			{text{"Bâtons de colle", "Glue sticks"}, 3, 1.20, catalog.AisleStationery},
			{text{"Ciseaux", "Scissors"}, 1, 3.90, catalog.AisleStationery},
			{text{"Agenda", "School planner"}, 1, 8.90, catalog.AisleStationery},
			{text{"Calculatrice", "Calculator"}, 1, 19.90, catalog.AisleElectronics},
			{text{"Gourde", "Water bottle"}, 1, 12.90, catalog.AisleHousehold},
		},
	},
	{
		key:  KeyHomeMaintenance,
		icon: "🏡",
		name: text{"Entretien de la maison / Bricolage", "Home Maintenance / DIY"},
		items: []item{
			{text{"Ampoules LED", "LED bulbs"}, 3, 4.90, catalog.AisleDIY},
			{text{"Joint silicone", "Silicone sealant"}, 1, 7.90, catalog.AisleDIY},
			{text{"Enduit de rebouchage", "Wall filler"}, 1, 8.50, catalog.AisleDIY},
			{text{"Peinture", "Paint"}, 1, 34.90, catalog.AisleDIY},
			{text{"Rouleau et pinceaux", "Roller and brushes"}, 1, 12.90, catalog.AisleDIY},
			{text{"Ruban de masquage", "Masking tape"}, 1, 4.50, catalog.AisleDIY},
			{text{"Bâche de protection", "Drop cloth"}, 1, 5.90, catalog.AisleDIY},
			{text{"Vis et chevilles", "Screws and wall plugs"}, 1, 9.90, catalog.AisleDIY},
			{text{"Papier de verre", "Sandpaper"}, 1, 4.90, catalog.AisleDIY},
			{text{"Dégrippant", "Penetrating oil"}, 1, 7.50, catalog.AisleDIY},
			{text{"Détecteur de fumée", "Smoke detector"}, 1, 19.90, catalog.AisleDIY},
			{text{"Terreau", "Potting soil"}, 1, 9.90, catalog.AisleDIY},
			{text{"Piles AA", "AA batteries"}, 1, 6.90, catalog.AisleElectronics},
			{text{"Filtre de hotte", "Cooker hood filter"}, 1, 12.90, catalog.AisleHousehold},
			{text{"Anti-calcaire", "Descaler"}, 1, 5.90, catalog.AisleHousehold},
		},
	},
	{
		key:  KeyCleaningTasks,
		icon: "🧹",
		name: text{"Tâches de Ménage & Entretien", "Cleaning & Upkeep Chores"},
		tasks: []task{
			{text{"Passer l'aspirateur", "Vacuum"}, weekly},
			{text{"Laver les sols", "Mop the floors"}, weekly},
			{text{"Dépoussiérer les meubles", "Dust the furniture"}, weekly},
			{text{"Nettoyer la salle de bain", "Clean the bathroom"}, weekly},
			{text{"Changer les draps", "Change the bed sheets"}, everyTwoWeeks},
			{text{"Laver les vitres", "Wash the windows"}, monthly},
			{text{"Nettoyer le réfrigérateur", "Clean the fridge"}, monthly},
			{text{"Vider le filtre du lave-linge", "Empty the washing machine filter"}, monthly},
			{text{"Nettoyer le filtre du lave-vaisselle", "Clean the dishwasher filter"}, monthly},
			{text{"Nettoyer les filtres de la hotte", "Clean the cooker hood filters"}, everyThreeMonths},
			{text{"Nettoyer les bouches de VMC", "Clean the ventilation vents"}, everyThreeMonths},
			{text{"Détartrer la bouilloire et la cafetière", "Descale the kettle and coffee maker"}, everyThreeMonths},
			{text{"Dégivrer le congélateur", "Defrost the freezer"}, everySixMonths},
			{text{"Tester les détecteurs de fumée", "Test the smoke detectors"}, everySixMonths},
			{text{"Ramoner la cheminée / entretenir la chaudière", "Sweep the chimney / service the boiler"}, yearly},
		},
	},
	{
		key:  KeyMovingTasks,
		icon: "📦",
		name: text{"To-Do Déménagement", "Moving To-Do"},
		tasks: []task{
			{text{"Donner son préavis au propriétaire", "Give notice to the landlord"}, ""},
			{text{"Réserver le camion ou le déménageur", "Book the van or the movers"}, ""},
			{text{"Résilier ou transférer les contrats d'énergie", "Cancel or transfer the energy contracts"}, ""},
			{text{"Transférer la box internet", "Transfer the internet subscription"}, ""},
			{text{"Mettre à jour l'assurance habitation", "Update the home insurance"}, ""},
			{text{"Faire suivre le courrier", "Set up mail forwarding"}, ""},
			{text{"Faire le changement d'adresse en ligne", "Report the change of address online"}, ""},
			{text{"Prévenir l'employeur, la banque et la CAF", "Notify the employer, the bank and the benefits office"}, ""},
			{text{"Relever les compteurs (eau, électricité, gaz)", "Read the meters (water, electricity, gas)"}, ""},
			{text{"Faire l'état des lieux de sortie", "Do the move-out inspection"}, ""},
			{text{"Faire l'état des lieux d'entrée", "Do the move-in inspection"}, ""},
			{text{"Mettre à jour la carte grise", "Update the vehicle registration"}, ""},
		},
	},
	{
		key:  KeyBackToSchoolTasks,
		icon: "🎒",
		name: text{"Check-list Rentrée & Papiers", "Back-to-School & Paperwork Checklist"},
		tasks: []task{
			{text{"Attestation d'assurance scolaire", "School insurance certificate"}, ""},
			{text{"Remplir la fiche sanitaire", "Fill in the health form"}, ""},
			{text{"Inscription à la cantine", "Register for the school canteen"}, ""},
			{text{"Inscription au périscolaire / à la garderie", "Register for after-school care"}, ""},
			{text{"Vérifier les vaccins dans le carnet de santé", "Check the vaccinations in the health record"}, ""},
			{text{"Faire les photos d'identité", "Get ID photos"}, ""},
			{text{"Récupérer la liste de fournitures", "Get the school supplies list"}, ""},
			{text{"Abonnement de transport scolaire", "School transport pass"}, ""},
			{text{"Questionnaire de santé ou certificat médical pour le sport", "Sports health questionnaire or medical certificate"}, ""},
			{text{"Inscription aux activités extrascolaires", "Sign up for after-school activities"}, ""},
			{text{"Mettre à jour les contacts d'urgence", "Update the emergency contacts"}, ""},
			{text{"Étiqueter les vêtements et les fournitures", "Label the clothes and supplies"}, ""},
		},
	},
}

// Builtins returns every built-in template in lang (any unsupported
// language falls back to French), in picker order, with its items.
func Builtins(lang string) []*models.ShoppingTemplate {
	templates := make([]*models.ShoppingTemplate, 0, len(builtins))
	for i := range builtins {
		templates = append(templates, builtins[i].toModel(lang))
	}
	return templates
}

// Builtin returns the built-in template with that key in lang, or false
// when there is none.
func Builtin(key, lang string) (*models.ShoppingTemplate, bool) {
	for i := range builtins {
		if builtins[i].key == key {
			return builtins[i].toModel(lang), true
		}
	}
	return nil, false
}

func (t *template) toModel(lang string) *models.ShoppingTemplate {
	if lang != "fr" && lang != "en" {
		lang = fallbackLanguage
	}
	m := &models.ShoppingTemplate{
		Key:      t.key,
		Builtin:  true,
		Name:     t.name.in(lang),
		Icon:     t.icon,
		ListType: purchaseListType,
		Items:    make([]*models.ShoppingTemplateItem, 0, len(t.items)+len(t.tasks)),
	}
	for _, it := range t.items {
		price := it.price
		m.Items = append(m.Items, &models.ShoppingTemplateItem{
			Title:          it.title.in(lang),
			Quantity:       it.quantity,
			EstimatedPrice: &price,
			Aisle:          it.aisle,
		})
	}
	if len(t.tasks) > 0 {
		m.ListType = taskListType
	}
	for _, tk := range t.tasks {
		m.Items = append(m.Items, &models.ShoppingTemplateItem{
			Title:          tk.title.in(lang),
			Quantity:       1,
			RecurrenceRule: tk.rule,
		})
	}
	FillTotals(m)
	return m
}

// FillTotals sets a template's ItemCount and EstimatedTotal (Σ quantity ×
// estimated price, to the cent) from its Items — for built-in and House
// templates alike.
func FillTotals(t *models.ShoppingTemplate) {
	total := 0.0
	for _, it := range t.Items {
		if it.EstimatedPrice == nil {
			continue
		}
		quantity := it.Quantity
		if quantity <= 0 {
			quantity = 1
		}
		total += *it.EstimatedPrice * float64(quantity)
	}
	t.ItemCount = len(t.Items)
	t.EstimatedTotal = budget.RoundCents(total)
}
