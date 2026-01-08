package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"context"
	"fmt"
	"io"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/transport"
)

// --- MODELOS ---

// Ofertas (Para el mapa)
type OfferResponse struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	Status      string  `json:"status"` // <--- NUEVO CAMPO: 'active', 'flash', 'suspended'
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Distance    float64 `json:"distance_meters"`
}

// Vehículos (Para el usuario)
type Vehicle struct {
	ID        string    `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	UserID    string    `gorm:"index" json:"user_id"`
	UserEmail string    `json:"user_email"`
	UserPhoto string    `json:"user_photo"`
	Type      string    `json:"type"` // 'car' o 'moto'
	Brand     string    `json:"brand"`
	Model     string    `json:"model"`
	Year      int       `json:"year"`
	IsActive  bool      `gorm:"default:true" json:"is_active"`
	Status    string    `gorm:"default:'SHADOW'" json:"status"` // 'SHADOW', 'ACTIVE', 'REJECTED'
	CreatedAt time.Time `json:"created_at"`
}

// Wallet (Billetera del usuario)
type Wallet struct {
	UserID         string  `gorm:"primaryKey" json:"user_id"`
	BalanceMoto    float64 `json:"balance_moto"`
	BalanceCar     float64 `json:"balance_car"`
	LifetimePoints float64 `json:"lifetime_points"`
	Goal           float64 `gorm:"default:500" json:"goal"`
	Status         string  `gorm:"default:'active'" json:"status"` // 'active', 'pending', 'frozen'
	LevelName      string  `gorm:"default:'Novato'" json:"level_name"`
}

// Location (Puntos cazados)
type Location struct {
	ID               string      `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	UserID           string      `gorm:"index" json:"user_id"`
	VehicleType      string      `json:"vehicle_type"` // 'moto' o 'car'
	ShopName         string      `json:"shop_name"`
	Category         string      `json:"category"`
	PhotoURL         string      `json:"photo_url"`
	Latitude         float64     `json:"latitude"`
	Longitude        float64     `json:"longitude"`
	Status           string      `gorm:"default:'pending'" json:"status"` // 'pending', 'approved', 'rejected'
	IsShadow         bool        `json:"is_shadow"`
	ActivationStatus string      `json:"activation_status"`
	AssetType        string      `json:"asset_type"`
	Geom             interface{} `gorm:"type:geography(POINT,4326)" json:"-"`
}

// Transaction (Historial de puntos)
type Transaction struct {
	ID          string    `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	UserID      string    `gorm:"index" json:"user_id"`
	VehicleType string    `json:"vehicle_type"` // 'moto' o 'car'
	Type        string    `json:"type"`         // 'earning'
	Amount      float64   `json:"points"`       // Cambiado de 'amount' a 'points' para el FE
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

var db *gorm.DB

func main() {
	_ = godotenv.Load()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("❌ Error: DATABASE_URL no configurada")
	}

	var err error
	db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("❌ Error DB:", err)
	}

	// 0. Asegurar PostGIS
	db.Exec("CREATE EXTENSION IF NOT EXISTS postgis;")

	// Migración automática (Crea tablas si no existen, útil como respaldo)
	db.AutoMigrate(&Vehicle{}, &Wallet{}, &Location{}, &Transaction{})

	// Fix: Eliminar constraint de user_id si existe (MVP mode)
	db.Exec("ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_user_id_fkey;")

	// --- MIGRACIÓN DE DATOS (PRODUCCIÓN) ---
	log.Println("🚀 Iniciando migración de datos para Status...")
	db.Exec("UPDATE locations SET status = 'approved' WHERE status IS NULL OR status = 'pending';")
	db.Exec("UPDATE offers SET status = 'active' WHERE status IS NULL;")
	log.Println("✅ Migración completada.")

	r := gin.Default()

	// CORS (Permitir acceso desde la App)
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "online ⚡"})
	})

	// --- RUTAS ---
	r.GET("/api/offers", getNearbyOffers)            // Buscar ofertas
	r.POST("/api/vehicles", createVehicle)           // Guardar vehículo
	r.GET("/api/vehicles/:user_id", getUserVehicles) // Consultar vehículos
	// Wallet
	// Wallet
	r.GET("/api/wallet/:user_id", getWallet)
	r.POST("/api/wallet/redeem", requestRedeem)
	// Hunter
	r.POST("/api/hunter/submit", submitHuntHandler)
	r.GET("/api/transactions/:user_id", getTransactions)

	// Admin
	r.GET("/api/admin/pending-vehicles", getPendingVehicles)
	r.POST("/api/admin/approve-vehicle", approveVehicle)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}

// --- CONTROLADORES ---

func createVehicle(c *gin.Context) {
	var v Vehicle
	if err := c.ShouldBindJSON(&v); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Inicializamos status según tipo
	if v.Type == "moto" || v.Type == "car" {
		v.Status = "SHADOW"
	} else {
		v.Status = "ACTIVE"
	}
	v.CreatedAt = time.Now()

	// Guardar en DB
	result := db.Create(&v)
	if result.Error != nil {
		c.JSON(500, gin.H{"error": "Error guardando vehículo"})
		return
	}
	c.JSON(201, v)
}

func getUserVehicles(c *gin.Context) {
	userID := c.Param("user_id")
	var vehicles []Vehicle
	db.Where("user_id = ?", userID).Find(&vehicles)
	c.JSON(200, vehicles)
}

func getNearbyOffers(c *gin.Context) {
	latStr := c.Query("lat")
	lngStr := c.Query("lng")
	radiusStr := c.Query("radius")

	if latStr == "" || lngStr == "" {
		c.JSON(400, gin.H{"error": "Faltan lat/lng"})
		return
	}

	lat, _ := strconv.ParseFloat(latStr, 64)
	lng, _ := strconv.ParseFloat(lngStr, 64)
	radius, _ := strconv.ParseFloat(radiusStr, 64)
	if radius == 0 {
		radius = 5000
	}

	var offers []OfferResponse
	// Consulta Geoespacial (UNION ALL entre Ofertas y Puntos Cazados)
	query := `
		(
			SELECT 
				id::text, 
				title, 
				description, 
				price, 
				category::text, -- Cast para el Enum de categoría
				status::text,   -- Cast para el Enum de status (ESTE ES EL VITAL)
				ST_Y(location::geometry) as latitude, 
				ST_X(location::geometry) as longitude,
				ST_Distance(location, ST_MakePoint(?, ?)::geography) as distance_meters
			FROM offers
			WHERE ST_DWithin(location, ST_MakePoint(?, ?)::geography, ?)
		)
		UNION ALL
		(
			SELECT 
				id::text, 
				shop_name as title, 
				'' as description, 
				0 as price, 
				category,        -- Ya es texto
				CASE 
					WHEN category IN ('station_moto', 'station_car') THEN 'shadow'
					WHEN status IS NULL OR status = '' THEN 'approved'
					ELSE status 
				END as status,   -- Aquí ya es texto
				latitude, 
				longitude,
				ST_Distance(geom, ST_MakePoint(?, ?)::geography) as distance_meters
			FROM locations
			WHERE ST_DWithin(geom, ST_MakePoint(?, ?)::geography, ?)
		)
		ORDER BY distance_meters ASC LIMIT 50;`

	db.Raw(query, lng, lat, lng, lat, radius, lng, lat, lng, lat, radius).Scan(&offers)
	c.JSON(200, offers)
}

func getWallet(c *gin.Context) {
	userID := c.Param("user_id")
	var wallet Wallet

	// Buscar billetera, si no existe, crearla
	if result := db.First(&wallet, "user_id = ?", userID); result.Error != nil {
		wallet = Wallet{
			UserID:         userID,
			BalanceMoto:    0,
			BalanceCar:     0,
			LifetimePoints: 0,
			Goal:           500,
			Status:         "active",
			LevelName:      "Novato",
		}
		db.Create(&wallet)
	}
	c.JSON(200, wallet)
}

func requestRedeem(c *gin.Context) {
	var req struct {
		UserID      string `json:"user_id"`
		VehicleType string `json:"vehicle_type"` // 'moto' o 'car'
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Falta datos (user_id/vehicle_type)"})
		return
	}

	var wallet Wallet
	if err := db.First(&wallet, "user_id = ?", req.UserID).Error; err != nil {
		c.JSON(404, gin.H{"error": "Wallet no encontrada"})
		return
	}

	currentBalance := wallet.BalanceMoto
	if req.VehicleType == "car" {
		currentBalance = wallet.BalanceCar
	}

	if currentBalance < wallet.Goal {
		c.JSON(400, gin.H{"error": "Saldo insuficiente en el modo seleccionado"})
		return
	}

	// Actualizar estado
	wallet.Status = "pending"
	db.Save(&wallet)

	c.JSON(200, gin.H{"message": "Solicitud recibida", "new_status": "pending"})
}

func submitHuntHandler(c *gin.Context) {
	// Ya no usamos JSON binding para este endpoint

	// --- SEGURIDAD DE PRODUCCIÓN ---
	// 1. Autorización: Administradores y Cazadores Oficiales
	authorizedUIDs := map[string]bool{
		"wkq951i7vvhJbrZOQmUav6B28BZ2": true, // Admin 1
		"DtfBh0Tr41fyjUwtcbl9WCBpgOJ2": true, // Usuario actual
	}

	// Como es multipart/form-data, leemos los campos uno por uno o usamos una estructura
	userID := c.PostForm("user_id")
	if !authorizedUIDs[userID] {
		c.JSON(http.StatusForbidden, gin.H{"error": "Acceso denegado: ID de usuario no autorizado para capturas oficiales"})
		return
	}

	shopName := c.PostForm("shop_name")
	category := c.PostForm("category")
	vehicleType := c.PostForm("vehicle_type")
	latStr := c.PostForm("latitude")
	lngStr := c.PostForm("longitude")
	isShadow := c.PostForm("is_shadow") == "true"
	activationStatus := c.PostForm("activation_status")
	assetType := c.PostForm("asset_type")

	lat, _ := strconv.ParseFloat(latStr, 64)
	lng, _ := strconv.ParseFloat(lngStr, 64)

	// 2. Validación estricta de categorías de producción
	allowedCategories := map[string]bool{
		"station_moto": true,
		"station_car":  true,
		"mechanic":     true,
		"parts":        true,
		"tires":        true,
		"oil":          true,
		"wash":         true,
		"tow":          true,
		"food":         true,
		"fuel_dollar":  true,
	}
	if !allowedCategories[category] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Categoría no permitida: " + category})
		return
	}

	// 0. PROXIMITY CHECK (Anti-Duplicado) - 20 metros
	var count int64
	checkQuery := `
		SELECT count(*) 
		FROM locations 
		WHERE category = ? 
		AND ST_DWithin(geom, ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, 20)`
	db.Raw(checkQuery, category, lng, lat).Scan(&count)

	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Este punto ya ha sido capturado recientemente"})
		return
	}
	// --- INICIALIZACIÓN DE GCS NIVEL SENIOR (Transporte Manual) ---
	photoUrl := ""
	file, err := c.FormFile("photo")
	if err == nil {
		ctx := context.Background()
		var client *storage.Client

		// 1. Leemos el archivo físico del Secret File
		data, errFile := os.ReadFile("firebase-key.json")
		if errFile != nil {
			log.Printf("❌ Error leyendo firebase-key.json: %v", errFile)
		} else {
			// 2. Creamos la identidad aislada
			creds, errCreds := google.CredentialsFromJSON(ctx, data, storage.ScopeFullControl)
			if errCreds != nil {
				log.Printf("❌ Error procesando credenciales: %v", errCreds)
			} else {
				// 3. CREAMOS UN CLIENTE HTTP YA AUTENTICADO
				// Esto evita que el SDK de Storage intente autenticarse por su cuenta
				hc, _, errTransport := transport.NewHTTPClient(ctx, option.WithCredentials(creds))
				if errTransport != nil {
					log.Printf("❌ Error creando transporte HTTP: %v", errTransport)
				} else {
					// 4. INICIALIZAMOS EL CLIENTE USANDO EL TRANSPORTE Y ANULANDO AUTH AUTOMÁTICA
					// El option.WithoutAuthentication() le dice al SDK: 'No busques llaves, usa el túnel hc'
					client, err = storage.NewClient(ctx, option.WithHTTPClient(hc), option.WithoutAuthentication())
				}
			}
		}

		// Validación de seguridad
		if err != nil || client == nil {
			log.Printf("❌ Error final creando cliente GCS: %v", err)
		} else {
			defer client.Close()

			bucketName := "chatcerex-v4-post-images"
			objectName := fmt.Sprintf("zona_flash/captures/%s/%d.jpg", userID, time.Now().Unix())

			f, openErr := file.Open()
			if openErr != nil {
				log.Printf("❌ Error abriendo archivo: %v", openErr)
			} else {
				defer f.Close()

				wc := client.Bucket(bucketName).Object(objectName).NewWriter(ctx)
				wc.ContentType = "image/jpeg"

				if _, err = io.Copy(wc, f); err != nil {
					log.Printf("❌ Error copiando a GCS: %v", err)
				}
				if err := wc.Close(); err != nil {
					log.Printf("❌ Error cerrando GCS writer: %v", err)
				} else {
					photoUrl = fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucketName, objectName)
					log.Printf("✅ FOTO SUBIDA EXITOSAMENTE: %s", photoUrl)
				}
			}
		}
	}

	tx := db.Begin()

	// 2. Insert Location
	loc := Location{
		UserID:           userID,
		VehicleType:      vehicleType,
		ShopName:         shopName,
		Category:         category,
		PhotoURL:         photoUrl,
		Latitude:         lat,
		Longitude:        lng,
		Status:           "pending",
		IsShadow:         isShadow,
		ActivationStatus: activationStatus,
		AssetType:        assetType,
	}

	// 1.1 Poblar Geom manualmente para PostGIS
	if err := tx.Create(&loc).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error creating location"})
		return
	}

	updateGeom := "UPDATE locations SET geom = ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography WHERE id = ?"
	if err := tx.Exec(updateGeom, lng, lat, loc.ID).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error updating geography"})
		return
	}

	// 3. Insert Transaction
	trans := Transaction{
		UserID:      userID,
		VehicleType: vehicleType,
		Amount:      10,
		Description: "Captura de negocio: " + shopName,
		CreatedAt:   time.Now(),
	}
	if err := tx.Create(&trans).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error creating transaction"})
		return
	}

	// 3. Upsert Wallet
	// Raw SQL para asegurar que el constraint no bloquee (MVP)
	tx.Exec("SET CONSTRAINTS ALL DEFERRED")

	// Usamos raw SQL para upsert atómico y sencillo
	// Determinamos qué balance actualizar según el vehicle_type
	balanceCol := "balance_moto"
	if vehicleType == "car" {
		balanceCol = "balance_car"
	}

	upsertWallet := `
        INSERT INTO wallets (user_id, balance_moto, balance_car, lifetime_points, goal, status, level_name) 
        VALUES (?, ?, ?, 10, 500, 'active', 'Novato') 
        ON CONFLICT (user_id) 
        DO UPDATE SET ` + balanceCol + ` = wallets.` + balanceCol + ` + 10, lifetime_points = wallets.lifetime_points + 10
    `

	balanceMotoInit := 0.0
	balanceCarInit := 0.0
	if vehicleType == "car" {
		balanceCarInit = 10.0
	} else {
		balanceMotoInit = 10.0
	}

	if err := tx.Exec(upsertWallet, userID, balanceMotoInit, balanceCarInit).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error updating wallet"})
		return
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("❌ Error al hacer COMMIT: %v", err)
	} else {
		log.Printf("✅ Transacción COMMIT exitosa para User: %s", userID)
	}

	// 5. Return updated wallet for instant FE sync
	var updatedWallet Wallet
	db.First(&updatedWallet, "user_id = ?", userID)

	c.JSON(200, gin.H{
		"message": "Hunt submitted successfully",
		"points":  10,
		"wallet":  updatedWallet,
	})
}

func getTransactions(c *gin.Context) {
	userID := c.Param("user_id")
	vehicleType := c.Query("vehicle_type") // Opcional: moto o car

	var transactions []Transaction

	query := db.Order("created_at DESC").Where("user_id = ?", userID)
	if vehicleType != "" {
		query = query.Where("vehicle_type = ?", vehicleType)
	}

	if err := query.Find(&transactions).Error; err != nil {
		c.JSON(500, gin.H{"error": "Error consultando transacciones"})
		return
	}

	c.JSON(200, transactions)
}
func getPendingVehicles(c *gin.Context) {
	var vehicles []Vehicle
	// Traemos SHADOW y ACTIVE para que el switch pueda operar en ambos sentidos
	db.Where("status IN ?", []string{"SHADOW", "ACTIVE"}).Order("created_at DESC").Limit(100).Find(&vehicles)
	c.JSON(200, vehicles)
}

func approveVehicle(c *gin.Context) {
	var req struct {
		VehicleID string `json:"vehicle_id"`
		Action    string `json:"action"` // 'approve' o 'reject'
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Faltan datos"})
		return
	}

	newStatus := "ACTIVE"
	if req.Action == "reject" {
		newStatus = "SHADOW" // Volver a shadow o manejar REJECTED
	}

	var vehicle Vehicle
	if err := db.First(&vehicle, "id = ?", req.VehicleID).Error; err != nil {
		c.JSON(404, gin.H{"error": "Vehículo no encontrado"})
		return
	}

	vehicle.Status = newStatus
	db.Save(&vehicle)

	// PUSH NOTIFICATION SIMULATION
	if newStatus == "ACTIVE" {
		log.Printf("🔔 [SILENT PUSH] Para Usuario %s (%s): ¡Cuenta activada! [Timestamp: %s]",
			vehicle.UserID, vehicle.UserEmail, time.Now().Format(time.RFC3339))
	} else {
		log.Printf("🔔 [SILENT PUSH] Para Usuario %s: Cuenta desactivada/Shadow.", vehicle.UserID)
	}

	c.JSON(200, gin.H{"message": "Estado actualizado", "new_status": newStatus})
}
